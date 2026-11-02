# Open-source Copper build (no hardware RAID / no enterprise license).
#
#   make cp-linux
#   make clean

GO ?= go
GOARCH ?= amd64

BMC_DIR := copper_bmc
CP_DIR  := copper_control_plane

BMC_CMD := ./cmd/copper_bmc/
CP_CMD  := ./cmd/copper_control_plane/

.PHONY: all linux windows clean bmc-linux bmc-windows cp-linux cp-windows fingerprint

all: linux windows

linux: bmc-linux cp-linux

windows: bmc-windows cp-windows

bmc-linux:
	@mkdir -p $(BMC_DIR)/bin
	cd $(BMC_DIR) && GOOS=linux GOARCH=$(GOARCH) $(GO) build -o ./bin/copper_bmc $(BMC_CMD)

bmc-windows:
	@mkdir -p $(BMC_DIR)/bin
	cd $(BMC_DIR) && GOOS=windows GOARCH=$(GOARCH) $(GO) build -o ./bin/copper_bmc.exe $(BMC_CMD)

cp-linux:
	@mkdir -p $(CP_DIR)/bin
	cd $(CP_DIR) && GOOS=linux GOARCH=$(GOARCH) $(GO) build -o ./bin/copper_control_plane $(CP_CMD)

cp-windows:
	@mkdir -p $(CP_DIR)/bin
	cd $(CP_DIR) && GOOS=windows GOARCH=$(GOARCH) $(GO) build -o ./bin/copper_cp.exe $(CP_CMD)

fingerprint: cp-linux
	$(CP_DIR)/bin/copper_control_plane fingerprint

clean:
	rm -f $(BMC_DIR)/bin/copper_bmc $(BMC_DIR)/bin/copper_bmc.exe
	rm -f $(CP_DIR)/bin/copper_control_plane $(CP_DIR)/bin/copper_cp.exe
