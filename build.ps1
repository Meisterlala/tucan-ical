$ErrorActionPreference = "Stop"

# Variables
$imageName = "registry.meisterlala.dev/tucan-ical"
$platforms = "linux/amd64,linux/arm64,linux/arm/v7"

# Ensure Buildx is available
docker buildx version | Out-Null

# Set up Docker Buildx builder
docker buildx create --use --name tucan-builder --driver docker-container --bootstrap 2>$null | Out-Null

# Fetch the latest version or initialize if not found
$latestVersion = git describe --tags --abbrev=0 2>$null
if (-Not $latestVersion) { $latestVersion = "0.1.0" }

# Increment the version number
$versionParts = $latestVersion -split '\.'
$versionParts[2] = [int]$versionParts[2] + 1
$newVersion = "$($versionParts[0]).$($versionParts[1]).$($versionParts[2])"
Write-Host "New version: $newVersion"

# Build and push the Docker image
docker buildx build --platform $platforms -t "${imageName}:${newVersion}" -t "${imageName}:latest" --push .
Write-Host "Build and push completed successfully."

# Push new Tags
git tag $newVersion
git push origin $newVersion
Write-Host "Pushed new tag: $newVersion"