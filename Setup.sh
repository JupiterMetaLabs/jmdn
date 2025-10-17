#!/bin/bash
# Setup script for JMZK Decentralized Network
# Installs all prerequisites in the correct order

echo "=== JMZK Decentralized Network Setup ==="
echo "Installing prerequisites..."

# Make prerequisite scripts executable
chmod +x ./Scripts/Go_Prerequisite.sh
chmod +x ./Scripts/ImmuDB_Prerequisite.sh
chmod +x ./Scripts/YGG_Prerequisite.sh
# chmod +x ./Scripts/Docker_Prerequisite.sh

# Install prerequisites in order
echo "1. Installing Go..."
./Scripts/Go_Prerequisite.sh

echo "2. Installing ImmuDB..."
./Scripts/ImmuDB_Prerequisite.sh

echo "3. Installing Yggdrasil..."
./Scripts/YGG_Prerequisite.sh

# Uncomment the line below if you need Docker
# echo "4. Installing Docker..."
# ./Scripts/Docker_Prerequisite.sh

echo "=== Setup Complete ==="
echo "All prerequisites have been installed successfully!"
echo "You can now build the application with: go build -o gossipnode"