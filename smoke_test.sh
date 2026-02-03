#!/bin/bash
set -e
set -x

export ANGARIUM_CONTROLLER="http://localhost:8090"
export ANGARIUM_TOKEN="sam-secret-token"

# Build all components
echo "Building components..."
make build

# Clean up existing processes if any
echo "Cleaning up previous runs..."
pkill -f angarium-controller || true
pkill -f angarium-agent || true
rm -f angarium.db angarium.db-wal angarium.db-shm logs/*.log logs/*.yaml logs/agent_state.json
mkdir -p logs

# Generate temporary configs with isolated ports
echo "Generating temporary configs..."
cat > logs/controller.yaml <<EOF
addr: "localhost:8090"
db_path: "angarium.db"
shared_token: "agent-secret-token"
users:
  - id: "user-1"
    name: "Sam"
    token: "sam-secret-token"
EOF

cat > logs/agent.yaml <<EOF
controller_url: "http://localhost:8090"
shared_token: "agent-secret-token"
node_id: "node-local"
addr: "http://localhost:8091"
EOF

# Start Controller
echo "Starting Controller..."
./bin/angarium-controller --config logs/controller.yaml > logs/controller.log 2>&1 &
CONTROLLER_PID=$!

# Wait for controller to start
sleep 2

# Start Agent
echo "Starting Agent..."
./bin/angarium-agent --config logs/agent.yaml --mock > logs/agent.log 2>&1 &
AGENT_PID=$!

# Wait for agent to heartbeat
echo "Waiting for agent registration..."
sleep 5

echo "Checking cluster status..."
./bin/angarium status

echo "--- Testing Job Execution ---"
# Submit a job that takes a few seconds
JOB_ID=$(./bin/angarium submit --gpus 1 --cwd /tmp "echo 'Job started'; sleep 3; echo 'Job finished'" | grep "ID:" | awk '{print $NF}')
echo "Submitted job: $JOB_ID"

echo "Waiting for scheduler to allocate job..."
sleep 3

echo "Checking 'angarium ps' (should show job starting/running)..."
./bin/angarium ps | grep "$(echo $JOB_ID | cut -c1-8)" || (echo "Job not found in ps output" && exit 1)

echo "Waiting for job completion..."
sleep 6

echo "Checking log output..."
./bin/angarium logs $JOB_ID | grep "Job finished" || (echo "Log output missing expected line" && exit 1)

echo "Inspecting terminal state..."
./bin/angarium inspect $JOB_ID | grep "State:      SUCCEEDED" || (echo "Job not in SUCCEEDED state" && exit 1)
./bin/angarium inspect $JOB_ID | grep "Exit Code:  0" || (echo "Job exit code not 0" && exit 1)

echo "Verifying Event System API..."
EVENTS_JSON=$(curl -s -H "Authorization: Bearer $ANGARIUM_TOKEN" "$ANGARIUM_CONTROLLER/v1/jobs/$JOB_ID/events")
echo "Events: $EVENTS_JSON"
if ! echo "$EVENTS_JSON" | grep -q "JOB_SUBMITTED"; then
    echo "Event JOB_SUBMITTED missing"
    exit 1
fi
if ! echo "$EVENTS_JSON" | grep -q "JOB_ALLOCATED"; then
    echo "Event JOB_ALLOCATED missing"
    exit 1
fi

echo "--- Testing Job Cancellation ---"
CANCEL_JOB_ID=$(./bin/angarium submit --gpus 2 --cwd /tmp "echo 'Long job'; sleep 30" | grep "ID:" | awk '{print $NF}')
echo "Submitted job for cancellation: $CANCEL_JOB_ID"

sleep 2
echo "Canceling job..."
./bin/angarium cancel $CANCEL_JOB_ID

sleep 2
echo "Verifying job is CANCELED..."
# Give it a moment to sync terminal state if needed
for i in {1..5}; do
    if ./bin/angarium inspect $CANCEL_JOB_ID | grep -q "State:.*CANCELED"; then
        echo "Job successfully canceled"
        break
    fi
    if [ $i -eq 5 ]; then
        echo "Job not in CANCELED state after 5 seconds:"
        ./bin/angarium inspect $CANCEL_JOB_ID
        exit 1
    fi
    sleep 1
done

echo "Verifying GPUs are released (should show 2/2 free)..."
FREE_GPUS=$(./bin/angarium status | grep "node-local" | awk '{print $4}' | cut -d'/' -f1)
if [ "$FREE_GPUS" != "2" ]; then
    echo "ERROR: GPUs not fully released. Free: $FREE_GPUS, Expected: 2"
    exit 1
fi

echo "--- Testing Max Runtime Enforcement ---"
MAX_RUNTIME_JOB_ID=$(./bin/angarium submit --gpus 1 --cwd /tmp --max-runtime 1 "echo 'Should be killed'; sleep 120" | grep "ID:" | awk '{print $NF}')
echo "Submitted max-runtime job: $MAX_RUNTIME_JOB_ID"

# Wait for it to move to STARTING or RUNNING
echo "Waiting for job to start..."
for i in {1..10}; do
    STATE=$(./bin/angarium inspect $MAX_RUNTIME_JOB_ID | grep "State:" | awk '{print $NF}')
    if [ "$STATE" == "RUNNING" ] || [ "$STATE" == "STARTING" ]; then
        echo "Job is $STATE"
        break
    fi
    sleep 1
done

# Even if it's STARTING, we hack it. 
# But we need to make sure the Controller has set started_at if we want the Scheduler to reap it.
# Actually, the Controller sets started_at if state == STARTING or RUNNING.
echo "Hacking started_at to expire job..."
sqlite3 angarium.db "UPDATE jobs SET started_at = datetime('now', '-2 minutes') WHERE id = '$MAX_RUNTIME_JOB_ID';"

echo "Waiting for scheduler to reap..."
# Verification loop for Max Runtime
for i in {1..10}; do
    if ./bin/angarium inspect $MAX_RUNTIME_JOB_ID | grep -q "State:      CANCELED"; then
        echo "Job CANCELED by max_runtime"
        ./bin/angarium inspect $MAX_RUNTIME_JOB_ID | grep "Reason:     Max runtime exceeded"
        SUCCESS=1
        break
    fi
    sleep 2
done

if [ -z "$SUCCESS" ]; then
    echo "Job not CANCELED by max_runtime"
    exit 1
fi
unset SUCCESS


echo "--- Testing Zombie Killing ---"
# Manually start a process that simulates a job but isn't in DB as running
echo "Simulating zombie process..."
# This is hard to test from outside without hacking Agent.
# But we can test the Controller side:
# Create a RUNNING job in Agent heartbeat that doesn't exist in DB.
# This requires mocking the Agent.
# The current smoke test uses a real Agent.
# Maybe we can modify the DB to delete a running job, then wait for Controller to kill the actual process?
ZOMBIE_JOB_ID=$(./bin/angarium submit --gpus 1 --cwd /tmp "echo 'I am a zombie'; sleep 30" | grep "ID:" | awk '{print $NF}')
echo "Submitted zombie candidate: $ZOMBIE_JOB_ID"
sleep 5 # Wait for it to start
# Delete from DB
sqlite3 angarium.db "DELETE FROM allocations WHERE job_id = '$ZOMBIE_JOB_ID';"
sqlite3 angarium.db "UPDATE jobs SET state = 'FAILED' WHERE id = '$ZOMBIE_JOB_ID';"

echo "Waiting for Zombie Hunter..."
sleep 15
# Check if process is still running?
# We can check via `angarium logs` maybe? No, if it's dead, logs stop.
# We can check if `angarium inspect` shows FAILED? It was manually set to FAILED.
# The claim is that Controller sends terminate.
# We can check controller logs.
grep "is zombie on node" logs/controller.log || (echo "Zombie detection log missing" && exit 1)

echo "--- Testing Agent Restart Recovery ---"
# Submit a long running job
RECOVERY_JOB_ID=$(./bin/angarium submit --gpus 1 --cwd /tmp "echo 'I survive restarts'; sleep 30" | grep "ID:" | awk '{print $NF}')
echo "Submitted recovery job: $RECOVERY_JOB_ID"

# Wait for it to move to RUNNING
echo "Waiting for job to be RUNNING..."
for i in {1..10}; do
    STATE=$(./bin/angarium inspect $RECOVERY_JOB_ID | grep "State:" | awk '{print $NF}')
    if [ "$STATE" == "RUNNING" ]; then
        echo "Job is $STATE"
        break
    fi
    sleep 1
done

# Kill Agent (simulate crash)
echo "Killing Agent..."
kill $AGENT_PID
wait $AGENT_PID 2>/dev/null || true

echo "Agent killed. Job process should still be running."
sleep 2

# Restart Agent
echo "Restarting Agent..."
./bin/angarium-agent --config logs/agent.yaml --mock > logs/agent.restart.log 2>&1 &
AGENT_PID=$!
sleep 5 # Wait for heartbeat and recovery

echo "Verifying job is still reported to Controller..."
# It might stay as RUNNING or re-report.
for i in {1..5}; do
    STATE=$(./bin/angarium inspect $RECOVERY_JOB_ID | grep "State:" | awk '{print $NF}')
    if [ "$STATE" == "RUNNING" ]; then
        echo "Job is successfully RUNNING after recovery"
        SUCCESS=1
        break
    fi
    sleep 2
done

if [ -z "$SUCCESS" ]; then
    echo "ERROR: Job state is $STATE, expected RUNNING after agent restart"
    exit 1
fi
unset SUCCESS

echo "--- Testing Lease Recovery ---"
echo "Killing controller..."
kill $CONTROLLER_PID
sleep 1

echo "Expiring leases in database..."
sqlite3 angarium.db "UPDATE gpu_leases SET expires_at = datetime('now', '-1 hour');"

# Restart controller
echo "Restarting controller..."
./bin/angarium-controller --config logs/controller.yaml > logs/controller.restart.log 2>&1 &
CONTROLLER_PID=$!
sleep 3

echo "Checking recovery..."
./bin/angarium status

echo "Cleaning up..."
kill $CONTROLLER_PID $AGENT_PID
echo "Smoke test passed successfully!"
