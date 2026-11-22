// Package imagecheck is the image check: it boots the image `make image`
// built in virtual machines and checks it end to end. The check itself is a
// test behind the imagecheck build tag (check_test.go); `make image-check`
// builds and runs it. The packages below it do the work: vm runs QEMU,
// guest reaches a machine over SSH, fixture makes disks and a release
// server, report prints the progress.
package imagecheck
