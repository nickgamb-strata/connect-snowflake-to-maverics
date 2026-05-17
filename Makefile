SHELL=/bin/bash -o pipefail

.PHONY: init up down logs smoke-test

init:
	mkdir -p ./certs
	CAROOT=./certs mkcert -install

	# Edge server cert (envoy - external traffic)
	CAROOT=./certs mkcert \
		-cert-file=./certs/edge-server.pem \
		-key-file=./certs/edge-server-key.pem \
		localhost "*.orchestrator.lab"

	# Internal server cert (services behind envoy)
	CAROOT=./certs mkcert \
		-cert-file=./certs/internal-server.pem \
		-key-file=./certs/internal-server-key.pem \
		"*.orchestrator.lab" 127.0.0.1

	# Keycloak server cert
	CAROOT=./certs mkcert \
		-cert-file=./certs/keycloak-server.pem \
		-key-file=./certs/keycloak-server-key.pem \
		keycloak.orchestrator.lab

	# Internal client cert for mTLS (envoy -> orchestrator)
	CAROOT=./certs mkcert -client \
		-cert-file=./certs/internal-client.pem \
		-key-file=./certs/internal-client-key.pem \
		envoy

	# OIDC Provider RSA key pair for JWT signing
	openssl genrsa -out ./certs/oidc-signing-key-00.pem 2048
	openssl rsa -in ./certs/oidc-signing-key-00.pem -pubout -out ./certs/oidc-signing-pub-00.pem

	# Configure local DNS resolver
	$(MAKE) dns-setup

	@echo ""
	@echo "==> Init complete. Copy .env.example to .env and set MAVERICS_IMAGE, then run: make up"

.PHONY: dns-setup
dns-setup:
	@case "$$(uname)" in \
		Darwin) \
			if [ -f /etc/resolver/orchestrator.lab ]; then \
				echo "DNS already configured: /etc/resolver/orchestrator.lab"; \
			else \
				echo "Configuring DNS resolver for orchestrator.lab (requires sudo)..."; \
				sudo mkdir -p /etc/resolver && \
				echo "nameserver 127.0.0.1" | sudo tee /etc/resolver/orchestrator.lab > /dev/null && \
				echo "Created /etc/resolver/orchestrator.lab"; \
			fi ;; \
		Linux) \
			if [ -f /etc/systemd/resolved.conf.d/orchestrator.lab.conf ]; then \
				echo "DNS already configured: /etc/systemd/resolved.conf.d/orchestrator.lab.conf"; \
			else \
				echo "Configuring DNS resolver for orchestrator.lab (requires sudo)..."; \
				sudo mkdir -p /etc/systemd/resolved.conf.d && \
				printf "[Resolve]\nDNS=127.0.0.1\nDomains=~orchestrator.lab\n" | \
					sudo tee /etc/systemd/resolved.conf.d/orchestrator.lab.conf > /dev/null && \
				sudo systemctl restart systemd-resolved && \
				echo "Created /etc/systemd/resolved.conf.d/orchestrator.lab.conf"; \
			fi ;; \
		*) \
			echo "Unsupported OS. Please manually configure DNS for orchestrator.lab to resolve via 127.0.0.1:53" ;; \
	esac

up:
	docker compose up -d --build

down:
	docker compose down --timeout 2 --volumes --remove-orphans

logs:
	docker compose logs -f

smoke-test:
	@echo "==> Checking Keycloak health..."
	@curl -sk https://keycloak.orchestrator.lab:8443/health/ready | grep -q UP && echo "  Keycloak: OK" || echo "  Keycloak: FAIL"
	@echo "==> Checking OIDC Provider discovery..."
	@curl -sk https://auth.orchestrator.lab/.well-known/oauth-authorization-server | grep -q issuer && echo "  OIDC Provider: OK" || echo "  OIDC Provider: FAIL"
	@echo "==> Checking Gateway requires auth..."
	@STATUS=$$(curl -sk -o /dev/null -w "%{http_code}" https://gateway.orchestrator.lab/mcp -H "Content-Type: application/json" -d '{"jsonrpc":"2.0","method":"initialize","id":1}'); \
		[ "$$STATUS" = "401" ] && echo "  Gateway auth: OK (401)" || echo "  Gateway auth: UNEXPECTED ($$STATUS)"
	@echo "==> Checking Protected Resource Metadata..."
	@curl -sk https://gateway.orchestrator.lab/.well-known/oauth-protected-resource | grep -q authorization_servers && echo "  Protected Resource Metadata: OK" || echo "  Protected Resource Metadata: FAIL"
