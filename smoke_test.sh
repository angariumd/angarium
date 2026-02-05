#!/bin/bash
set -e

export ANGARIUM_CONTROLLER="http://localhost:8090"
export ANGARIUM_TOKEN="sam-secret-token"

function cleanup {
    pkill -f angarium-controller || true
    pkill -f angarium-agent || true
    rm -f angarium.db* logs/*.log logs/*.yaml logs/agent_state.json
}
trap cleanup EXIT

echo "Building..."
make build

mkdir -p logs

# Generate configs
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

echo "Starting services..."
./bin/angarium-controller --config logs/controller.yaml > logs/controller.log 2>&1 &
sleep 2

./bin/angarium-agent --config logs/agent.yaml --mock > logs/agent.log 2>&1 &
sleep 5 # wait for registration

./bin/angarium status

function wait_for_state {
    local job_id=$1
    local target_state=$2
    local timeout=${3:-10}
    
    for ((i=1; i<=timeout; i++)); do
        if ./bin/angarium inspect $job_id | grep -q "State:.*$target_state"; then
            return 0
        fi
        sleep 1
    done
    echo "Timeout waiting for job $job_id to reach $target_state"
    return 1
}

echo "--- Job Execution ---"
JOB_ID=$(./bin/angarium submit --gpus 1 --cwd /tmp "echo 'Job started'; sleep 3; echo 'Job finished'" | grep "ID:" | awk '{print $NF}')
echo "Job: $JOB_ID"

wait_for_state "$JOB_ID" "SUCCEEDED" 15
./bin/angarium logs "$JOB_ID" | grep "Job finished" || exit 1

echo "Verifying Events..."
curl -s -H "Authorization: Bearer $ANGARIUM_TOKEN" "$ANGARIUM_CONTROLLER/v1/jobs/$JOB_ID/events" | grep -q "JOB_SUBMITTED" || exit 1

echo "--- Job Cancellation ---"
CANCEL_JOB_ID=$(./bin/angarium submit --gpus 2 --cwd /tmp "echo 'Long job'; sleep 30" | grep "ID:" | awk '{print $NF}')
sleep 2
./bin/angarium cancel "$CANCEL_JOB_ID"
wait_for_state "$CANCEL_JOB_ID" "CANCELED"

# Verify resource release
FREE_GPUS=$(./bin/angarium status | grep "node-local" | awk '{print $4}' | cut -d'/' -f1)
if [ "$FREE_GPUS" != "2" ]; then
    echo "ERROR: GPUs not released. Free: $FREE_GPUS, Expected: 2"
    exit 1
fi

echo "--- Max Runtime ---"
MAX_JOB_ID=$(./bin/angarium submit --gpus 1 --cwd /tmp --max-runtime 1 "sleep 120" | grep "ID:" | awk '{print $NF}')
wait_for_state "$MAX_JOB_ID" "RUNNING" 
# Hack started_at to simulate expiry
sqlite3 angarium.db "UPDATE jobs SET started_at = datetime('now', '-2 minutes') WHERE id = '$MAX_JOB_ID';"
wait_for_state "$MAX_JOB_ID" "CANCELED"

echo "--- Zombie Handling ---"
ZOMBIE_ID=$(./bin/angarium submit --gpus 1 --cwd /tmp "echo 'Zombie'; sleep 30" | grep "ID:" | awk '{print $NF}')
sleep 5
# Simulate zombie by deleting allocation record but leaving process running
sqlite3 angarium.db "DELETE FROM allocations WHERE job_id = '$ZOMBIE_ID';"
sqlite3 angarium.db "UPDATE jobs SET state = 'FAILED' WHERE id = '$ZOMBIE_ID';"
sleep 15
grep "zombie on node" logs/controller.log || exit 1

echo "--- Agent Recovery ---"
RECOVERY_ID=$(./bin/angarium submit --gpus 1 --cwd /tmp "sleep 30" | grep "ID:" | awk '{print $NF}')
wait_for_state "$RECOVERY_ID" "RUNNING"
pkill -f angarium-agent
sleep 2
./bin/angarium-agent --config logs/agent.yaml --mock > logs/agent.restart.log 2>&1 &
sleep 5
wait_for_state "$RECOVERY_ID" "RUNNING"

echo "PASS"
