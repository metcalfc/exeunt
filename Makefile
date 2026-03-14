HOST ?= exeunt.exe.xyz
BINARY = exeunt-autoscaler
BUILD_DIR = cmd/autoscaler
REMOTE_BIN = /usr/local/bin/$(BINARY)
SERVICE = exeunt-autoscaler
CONFIG = $(shell test -f deploy/config.local.json && echo deploy/config.local.json || echo deploy/config.json)

.PHONY: build test test-integration bats lint check deploy deploy-monitor deploy-alert-responder deploy-all start stop restart status logs clean

build:
	cd $(BUILD_DIR) && GOOS=linux GOARCH=amd64 go build -o ../../$(BINARY) .

test:
	cd $(BUILD_DIR) && go test -short -v -count=1 -race ./...

test-integration:
	cd $(BUILD_DIR) && go test -v -count=1 -timeout 5m ./...

bats:
	bats tests/

lint:
	actionlint
	shellcheck scripts/*.sh

check: lint test bats

deploy: build
	scp $(BINARY) $(HOST):/tmp/$(BINARY)
	scp deploy/exeunt-autoscaler.service $(HOST):/tmp/$(SERVICE).service
	scp $(CONFIG) $(HOST):/tmp/autoscaler-config.json
	ssh $(HOST) 'sudo systemctl stop $(SERVICE) 2>/dev/null || true'
	ssh $(HOST) 'sudo mv /tmp/$(BINARY) $(REMOTE_BIN) && sudo chmod +x $(REMOTE_BIN)'
	ssh $(HOST) 'sudo mv /tmp/$(SERVICE).service /etc/systemd/system/$(SERVICE).service'
	ssh $(HOST) 'sudo mkdir -p /etc/exeunt-autoscaler && sudo mv /tmp/autoscaler-config.json /etc/exeunt-autoscaler/config.json && sudo chmod 644 /etc/exeunt-autoscaler/config.json'
	ssh $(HOST) 'sudo mkdir -p /var/lib/exeunt-autoscaler && sudo chown exedev /var/lib/exeunt-autoscaler'
	ssh $(HOST) 'sudo systemctl daemon-reload && sudo systemctl enable $(SERVICE) && sudo systemctl start $(SERVICE)'
	ssh $(HOST) 'sleep 2 && sudo systemctl status $(SERVICE) --no-pager'
	rm -f $(BINARY)

deploy-monitor:
	scp scripts/monitor.sh $(HOST):/tmp/exeunt-monitor
	scp deploy/exeunt-monitor.service $(HOST):/tmp/exeunt-monitor.service
	scp deploy/exeunt-monitor.timer $(HOST):/tmp/exeunt-monitor.timer
	ssh $(HOST) 'sudo mv /tmp/exeunt-monitor /usr/local/bin/exeunt-monitor && sudo chmod +x /usr/local/bin/exeunt-monitor'
	ssh $(HOST) 'sudo mv /tmp/exeunt-monitor.service /etc/systemd/system/exeunt-monitor.service'
	ssh $(HOST) 'sudo mv /tmp/exeunt-monitor.timer /etc/systemd/system/exeunt-monitor.timer'
	ssh $(HOST) 'sudo systemctl daemon-reload && sudo systemctl enable exeunt-monitor.timer && sudo systemctl start exeunt-monitor.timer'
	ssh $(HOST) 'sudo systemctl list-timers exeunt-monitor.timer --no-pager'

deploy-alert-responder:
	scp scripts/alert-responder.sh $(HOST):/tmp/exeunt-alert-responder
	scp deploy/exeunt-alert-responder.service $(HOST):/tmp/exeunt-alert-responder.service
	scp deploy/exeunt-alert-responder.path $(HOST):/tmp/exeunt-alert-responder.path
	ssh $(HOST) 'sudo mv /tmp/exeunt-alert-responder /usr/local/bin/exeunt-alert-responder && sudo chmod +x /usr/local/bin/exeunt-alert-responder'
	ssh $(HOST) 'sudo mv /tmp/exeunt-alert-responder.service /etc/systemd/system/exeunt-alert-responder.service'
	ssh $(HOST) 'sudo mv /tmp/exeunt-alert-responder.path /etc/systemd/system/exeunt-alert-responder.path'
	ssh $(HOST) 'sudo systemctl daemon-reload && sudo systemctl enable exeunt-alert-responder.path && sudo systemctl start exeunt-alert-responder.path'
	ssh $(HOST) 'sudo systemctl status exeunt-alert-responder.path --no-pager'

deploy-all: deploy deploy-monitor deploy-alert-responder

start:
	ssh $(HOST) sudo systemctl start $(SERVICE)

stop:
	ssh $(HOST) sudo systemctl stop $(SERVICE)

restart: build
	scp $(BINARY) $(HOST):/tmp/$(BINARY)
	ssh $(HOST) 'sudo systemctl stop $(SERVICE) && sudo mv /tmp/$(BINARY) $(REMOTE_BIN) && sudo chmod +x $(REMOTE_BIN) && sudo systemctl start $(SERVICE)'
	rm -f $(BINARY)

status:
	ssh $(HOST) sudo systemctl status $(SERVICE) --no-pager

logs:
	ssh $(HOST) sudo journalctl -u $(SERVICE) --no-pager -n 50

clean:
	rm -f $(BINARY)
