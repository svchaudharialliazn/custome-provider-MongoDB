#!/bin/bash

# Export environment variables with fixed version
export PROJECT_NAME=swapnil-provider-mongodb
export PROJECT_REPO=github.com/svchaudharialliazn/swapnil-provider-mongodb
export VERSION=v0.1.10
export DOCKERHUB_ORG=svchaudharialliazn
export REGISTRY=docker.io
export PLATFORMS=linux/amd64,linux/arm64,windows/amd64,darwin/amd64,darwin/arm64

# Print configuration
echo "Building with configuration:"
echo "PROJECT_NAME: $PROJECT_NAME"
echo "PROJECT_REPO: $PROJECT_REPO"
echo "VERSION: $VERSION"
echo "DOCKERHUB_ORG: $DOCKERHUB_ORG"
echo "REGISTRY: $REGISTRY"
echo "PLATFORMS: $PLATFORMS"

# Function to check command status
check_status() {
    if [ $? -eq 0 ]; then
        echo "✅ $1 completed successfully"
    else
        echo "❌ $1 failed"
        exit 1
    fi
}

# Clean previous builds
echo "🧹 Cleaning previous builds..."
make clean
check_status "Clean"

# Update dependencies
echo "📦 Updating dependencies..."
make vendor
check_status "Vendor"

# Generate CRDs and code
echo "🔧 Generating CRDs and code..."
make generate
check_status "Generate"

# Build binary
echo "🔨 Building binary..."
make build
check_status "Build"

# Build Docker image
echo "-----------------------------------🐳 Building Docker image... --------------------------------------"
docker build -t $REGISTRY/$DOCKERHUB_ORG/swap-provider-mongodb-controller:$VERSION .
check_status "Docker build"

# Push Docker image
echo ""-----------------------------------⬆️ Pushing Docker image... "-----------------------------------"
docker push $REGISTRY/$DOCKERHUB_ORG/swap-provider-mongodb-controller:$VERSION
check_status "Docker push"

# Build package
echo ""----------------------------------- 📦 Building package... "-----------------------------------"
make build.xpkg
check_status "Build package"

# Push package
echo ""-----------------------------------⬆️ Pushing package... "-----------------------------------"
make push.xpkg
check_status "Push package"

echo ""----------------------------------- 🎉 Build process completed successfully!" -----------------------------------"
echo "Version $VERSION has been built and pushed"

# Optional: Create a git tag
read -p "Do you want to create a git tag for version $VERSION? (y/n) " -n 1 -r
echo
if [[ $REPLY =~ ^[Yy]$ ]]
then
    git tag $VERSION
    git push origin $VERSION
    echo "✅ Git tag $VERSION created and pushed"
fi
