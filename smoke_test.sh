#!/bin/bash
set -e

# Build all components
echo "Building components..."
make build

# Clean up existing processes if any
killall controller agent 2>/dev/null || true
rm -f angarium.db

# Start Controller
echo "Starting Controller..."
./bin/controller --config config/controller.yaml > controller.log 2>&1 &
CONTROLLER_PID=$!

# Wait for controller to start
sleep 2

# Start Agent
echo "Starting Agent..."
./bin/agent --config config/agent.yaml > agent.log 2>&1 &
AGENT_PID=$!

# Wait for agent to heartbeat
sleep 7

echo "Checking cluster status..."
./bin/cli status

echo "Submitting job (2 GPUs)..."
./bin/cli submit --gpus 2 --cwd /tmp -- python -c "print('hello Angarium')"

echo "Checking queue..."
./bin/cli queue

# Wait for scheduler to run
echo "Waiting for scheduler..."
sleep 3

echo "Checking queue after allocation..."
./bin/cli queue

echo "Submitting a job that is too large (10 GPUs)..."
./bin/cli submit --gpus 10 --cwd /tmp -- python -c "print('too big')"

echo "Waiting for scheduler..."
sleep 3

echo "Inspecting queued job..."
./bin/cli queue # Show list with reason
# Get the ID of the large job (it will be the last one)
LARGE_JOB_ID=$(./bin/cli queue | grep "too big" | awk '{print $1}')
./bin/cli inspect $LARGE_JOB_ID

echo "--- Testing Lease Recovery ---"
echo "Killing controller to simulate crash..."
kill $CONTROLLER_PID
sleep 2

echo "Expiring leases in database..."
sqlite3 angarium.db "UPDATE gpu_leases SET expires_at = datetime('now', '-1 hour');"

echo "Restarting controller..."
./bin/controller --config config/controller.yaml > controller.restart.log 2>&1 &
CONTROLLER_PID=$!
sleep 5

echo "Checking if GPUs are recovered (should show 2/2 free)..."
./bin/cli status

echo "Cleaning up..."
kill $CONTROLLER_PID $AGENT_PID
echo "Smoke test finished."
