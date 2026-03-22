#!/usr/bin/env bash

# scripts/load_test.sh creates multiple fake camera streams using ffmpeg to stress test the server

CAMS=${1:-10}
SERVER_URL="rtmp://localhost:1935"

echo "Starting $CAMS fake camera streams to $SERVER_URL..."
echo "Press Ctrl+C to stop all streams, or use 'killall ffmpeg' later."

# Store PIDs
PIDS=""

for i in $(seq 1 $CAMS); do
    STREAM_KEY="cam_$i"
    # Create low resolution, low framerate streams to focus load on the generic routing/fanout layer
    # rather than just overloading ffmpeg's encode layer limits
    ffmpeg -re -f lavfi -i "testsrc=size=640x360:rate=15" -c:v libx264 -preset ultrafast -tune zerolatency -f flv "$SERVER_URL/$STREAM_KEY" > /dev/null 2>&1 &
    PIDS="$PIDS $!"
done

echo "All $CAMS streams started in background (PIDs: $PIDS)."
echo "You can now run: go tool pprof http://localhost:8080/debug/pprof/profile?seconds=20"

# Wait for all background streams
trap "echo 'Stopping all streams...'; kill $PIDS 2>/dev/null; exit 0" INT TERM
wait $PIDS
