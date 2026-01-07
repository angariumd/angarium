#!/bin/bash
set -e

# Build all components
echo "Building components..."
make build

# Clean up existing processes if any
killall controller agent 2>/dev/null || true
rm -f angarium.db angarium.db-wal angarium.db-shm logs/*.log
mkdir -p logs

# Start Controller
echo "Starting Controller..."
./bin/controller --config config/controller.yaml > logs/controller.log 2>&1 &
CONTROLLER_PID=$!

# Wait for controller to start
sleep 2

# Start Agent
echo "Starting Agent..."
./bin/agent --config config/agent.yaml > logs/agent.log 2>&1 &
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

echo "Checking 'gpu ps' (should show job starting/running)..."
./bin/angarium ps | grep "${JOB_ID:0:8}" || (echo "Job not found in ps output" && exit 1)

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
./bin/angarium inspect $CANCEL_JOB_ID | grep "State:      CANCELED" || (echo "Job not in CANCELED state" && exit 1)

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

echo "Restarting controller..."
./bin/controller --config config/controller.yaml > logs/controller.restart.log 2>&1 &
CONTROLLER_PID=$!
sleep 3

echo "Checking recovery..."
./bin/angarium status

echo "Cleaning up..."
kill $CONTROLLER_PID $AGENT_PID
echo "Smoke test passed successfully!"
