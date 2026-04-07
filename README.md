# Tucan iCal

A web service that fetches calendar data from TU Darmstadt and exports it as an iCal (.ics) file, which can be imported into calendar applications like Google Calendar, Outlook, or Apple Calendar.

> You need to host this somewhere with a publically avalible URL! Use a ddns service and point it to your PC or buy a real domain. This doesnt need many resources, it runs well on a raspberry pi

## Features

- Fetches calendar data from TU Darmstadt Tucan
- Exports calendar data in standard iCal format
- REST API endpoint for retrieving the calendar
- Docker containerized for easy deployment

## Prerequisites

- Go 1.24+ (for local development)
- Docker (for containerized builds and runs)

## How to Run

1. Clone the repository:

   ```bash
   git clone <repository-url>
   cd tucan-ical
   ```

2. Install dependencies:

   ```bash
   go mod download
   ```

3. Create a `.env` file based on `.env.example` and configure your environment variables. Here you need to **set your login information** for tucan

4. Run the application:
   ```bash
   go run .
   ```
   The server will start on port 8080.

## Docker Usage

### Running the Docker Image

You can build the Dockerfile and run the container like this:

```bash
docker build -t tucan-ical .
```

### Environment Variables

You can pass environment variables to the Docker container and run it.

```bash
docker run -p 8080:8080 -e TUCAN_USERNAME=abc -e TUCAN_PASSWORD=123 -e TUCAN_TOTP=BASE32SECRET tucan-ical
```

You can then look at the exported .ical at `localhost:8080/tucan.ics`

## Kubernetes Deployment

The project includes Kubernetes manifests in the `k8s/` directory. You can use it as a template if needed, it won't work without modifications

## API Usage

### Get Calendar

Retrieve the calendar data in iCal format:

```
GET /tucan.ics
```

Response: iCal (.ics) file content that can be imported into calendar applications.

## Configuration

The application uses environment variables for configuration. Refer to `.env.example` for available options.
