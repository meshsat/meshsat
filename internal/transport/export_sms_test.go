package transport

import (
	"sync"
	"time"

	"go.bug.st/serial"
)

// Fixtures for the external transport_test package: the SMS bundle tests
// run the real gateway, the real bundle counter and this real I/O loop on
// an injected modem (B9FIX-R1, R2, R3). Test files only. [MESHSAT-1584]

// StartSMSLoopForTest runs the I/O loop on p with short timeouts: the
// caller gives up after a second, the network's answer is waited for
// answer. stop ends the loop, restores the timeouts and reports whether
// the loop ended.
func StartSMSLoopForTest(p serial.Port, answer time.Duration) (*DirectCellTransport, func() bool) {
	prevSubmit, prevGrace, prevSettle, prevAnswer := smsSubmitTimeout, cmdResponseGrace, smsSendSettle, smsAnswerTimeout
	smsSubmitTimeout, cmdResponseGrace, smsSendSettle, smsAnswerTimeout = time.Second, 50*time.Millisecond, 0, answer
	tr := NewDirectCellTransport("/dev/null")
	tr.file = p
	tr.running = true
	tr.cmdCh = make(chan atCommand, 16)
	tr.stopCh = make(chan struct{})
	tr.ioDone = make(chan struct{})
	tr.sess = &cellSession{cmdCh: tr.cmdCh, stop: tr.stopCh, done: tr.ioDone, file: p}
	go tr.ioLoop()
	return tr, func() bool {
		close(tr.stopCh)
		stopped := false
		select {
		case <-tr.ioDone:
			stopped = true
		case <-time.After(3 * time.Second):
		}
		smsSubmitTimeout, cmdResponseGrace, smsSendSettle, smsAnswerTimeout = prevSubmit, prevGrace, prevSettle, prevAnswer
		return stopped
	}
}

// HoldSMSLoopForTest occupies the I/O loop with a command that runs until
// release is called; done closes when it has ended.
func HoldSMSLoopForTest(tr *DirectCellTransport) (release func(), done <-chan struct{}) {
	started, rel, fin := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		_, _ = tr.execRawFn(func(serial.Port) (string, error) {
			close(started)
			<-rel
			return "", nil
		}, 10*time.Second)
		close(fin)
	}()
	<-started
	var once sync.Once
	return func() { once.Do(func() { close(rel) }) }, fin
}

// SMSQueueLenForTest is how many commands wait for the I/O loop.
func SMSQueueLenForTest(tr *DirectCellTransport) int { return len(tr.cmdCh) }
