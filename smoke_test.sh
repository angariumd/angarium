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
rm -f angarium.db angarium.db-wal angarium.db-shm logs/*.log logs/*.yaml
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

echo "--- Testing Lease Recovery (Legacy) ---"
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
