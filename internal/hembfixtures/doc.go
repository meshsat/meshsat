// Package hembfixtures checks this repo's copy of the HeMB-NG fixture release
// (contract C12, MESHSAT-1584): the test vectors the Bridge, the Hub and the
// phones share. The release lives in meshsat-hub docs/fixtures/v1; the copy is
// testdata/v1 and fixtures.lock names the release it must be. No code of its
// own; the tests that consume the vectors live with the code they test.
package hembfixtures
