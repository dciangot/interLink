# mTLS Startup Guide

## Introduction
This guide provides comprehensive documentation on the mTLS (Mutual Transport Layer Security) startup script used in the InterLink project. The mTLS startup script is essential for ensuring secure communication between clients and servers in a microservices architecture.

## Prerequisites
Before running the mTLS startup script, ensure you have the following prerequisites:
- Docker installed on your machine.
- Access to the InterLink repository.
- Proper configuration for your certificates.

## Setup Steps
1. **Clone the Repository**
   ```bash
   git clone https://github.com/interlink-hq/interLink.git
   cd interLink
   ```

2. **Generate Certificates**  
   To establish a secure connection, you must generate the necessary certificates:
   ```bash
   openssl req -x509 -nodes -days 365 -newkey rsa:2048 -keyout client-key.pem -out client-cert.pem
   ```

3. **Configure the Startup Script**  
   Modify the `startup.sh` script to include your certificate and key:
   ```bash
   #!/bin/bash
   export CA_CERT=path/to/ca-cert.pem
   export CLIENT_CERT=path/to/client-cert.pem
   export CLIENT_KEY=path/to/client-key.pem
   ```

4. **Run the mTLS Startup Script**  
   Execute the script to start your services with mTLS enabled:
   ```bash
   ./startup.sh
   ```

## Troubleshooting
- If you encounter issues, check the log files generated during startup for any error messages related to certificate validation or service communication.
- Ensure that the service endpoints are correctly configured to accept mTLS connections.

## Conclusion
This mTLS startup guide ensures that you can securely communicate within the InterLink project. Following these steps will help you set up and troubleshoot your mTLS configuration efficiently.