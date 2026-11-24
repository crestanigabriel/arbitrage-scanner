// Package contract checks the protobuf contracts shared with the Python detector.
//
// Its tests compare reference messages with the golden files in fixtures/contracts at the repo
// root. The detector's tests decode the same files, so both languages read the same bytes the
// same way. Run `go test ./internal/contract -update` to rewrite the files after a schema change.
package contract
