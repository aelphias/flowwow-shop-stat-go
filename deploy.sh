#!/bin/bash

# Load configuration from .env file
if [ -f .env ]; then
    export $(grep -v '^#' .env | xargs)
fi

BINARY_NAME="flowwow-stats-linux"

echo "Building for Linux (amd64)..."
GOOS=linux GOARCH=amd64 go build -o $BINARY_NAME main.go

echo "Uploading files to server..."
scp $BINARY_NAME shops.txt .env flowwow-stats.service flowwow-stats.timer $SERVER_USER@$SERVER_IP:~/

echo "Done! Run './$BINARY_NAME' on the server to test."
