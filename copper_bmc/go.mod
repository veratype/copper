module copper_bmc

go 1.26.0

require (
	copper_dhcp v0.0.0
	github.com/google/gopacket v1.1.19
	github.com/stmcginnis/gofish v0.27.0
	golang.org/x/net v0.55.0
)

require (
	github.com/insomniacslk/dhcp v0.0.0-20261014062924-c6bb46ad3f3a // indirect
	github.com/josharian/native v1.1.0 // indirect
	github.com/pierrec/lz4/v4 v4.1.14 // indirect
	github.com/u-root/uio v0.0.0-20230220225925-ffce2a382923 // indirect
	golang.org/x/sys v0.45.0 // indirect
)

replace copper_dhcp => ../copper_dhcp
