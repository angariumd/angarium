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

echo "Checking nodes..."
./bin/cli nodes

echo "Submitting job..."
./bin/cli submit --gpus 2 --cwd /tmp -- python -c "print('hello Angarium')"

echo "Checking queue..."
./bin/cli queue

# Clean up
echo "Cleaning up..."
kill $CONTROLLER_PID $AGENT_PID
echo "Smoke test finished."
