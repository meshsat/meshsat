# The safety chain, proven by tests

SIDN fonds project 261042, deliverable D2: an SOS carries the sender's position and reaches the satellite on both modem types through the delivery ledger, and the dead man's switch runs in the shipped binary. Four tests, merged and green in the build pipeline.

| Test | What it proves |
|---|---|
| `TestSafetyChain1_TheSOSCarriesTheSendersPosition` (`internal/api/sos_safety_chain_test.go`) | The satellite frame of an SOS carries the position it was raised with. For the dead man's switch with no GPS fix of the Bridge's own, it carries the last position the Bridge knew. With no position at all, the words of the SOS say the position is unknown, so 0, 0 is not taken for a place. |
| `TestSafetyChain2_TheSOSReachesTheSBDModemThroughTheLedger` (same file) | An SOS becomes one row in the delivery ledger on the Iridium SBD link (priority 0, tried until it goes), the delivery worker hands its frame to the modem, the frame decodes to the position, and the row ends as sent or delivered. |
| `TestSafetyChain3_TheSOSReachesTheIMTModemThroughTheLedger` (same file) | The same for the RockBLOCK 9704 (Iridium IMT). |
| `TestSafetyChain4_TheShippedBinaryArmsTheDeadMansSwitch` (`cmd/meshsat/deadman_wiring_test.go`) | The program the field kits run builds the dead man's switch, hands it to the API that arms it and starts it. Off as shipped, it does nothing; on and past its timeout it raises the SOS once, with its last known position, and the TAK alarm. A source check fails the build if the program stops building it this way. |

The modems in these tests are fakes that record what they are given: no satellite session opens, and the satellite link itself is not part of them. They keep the chain from the button to the modem from breaking unnoticed, on every build.

One limit found while writing them: a long SOS text with no position can lose the words "position unknown" at the frame's 64-byte text limit. The short test text keeps them. The fix is open, for the Bridge and both phone apps together, since they share the frame.

## The build

- **The tests:** commit `e16d69ce69c3832b921531d945869f19e82cd6e6`.
- **Green in the pipeline:** pipeline 64497 (commit `b4ad2ba6813de547b90cf44dc28a506db34475cc`, which carries them), job 628489 `test`: every package passed, `internal/api` and `cmd/meshsat` among them. The same pipeline deployed to both field kits on 13 November 2026.
- **On every build since:** the `test` job first runs the four by name, so each one shows passed or failed in its log, then runs every other test. A failure in any of them stops the deploy to the field kits.
