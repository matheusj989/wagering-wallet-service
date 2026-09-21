// Package failpoint kills the process on purpose at a named point, so a test can
// prove the service recovers from dying at the worst possible moment.
//
// Nothing here does anything unless the binary is built with the failpoints tag
// and the FAILPOINT variable names one of these points:
//
//	consumer.after_commit_before_delete      the transaction committed but the queue
//	                                         message was not deleted, so it will be
//	                                         redelivered and must replay, not repeat
//	outbox.after_publish_before_mark         the event reached the queue but was not
//	                                         marked as published, so it will be sent
//	                                         again and the consumer must deduplicate
//	wagering.after_pending_reference_commit  the operation was parked waiting for its
//	                                         reference and the instance died before
//	                                         answering, so another instance must pick
//	                                         the work up from the database
//
// The delivered binary is built without the tag, and then Hit is an empty function
// the compiler removes.
package failpoint

type Failpoint struct{}

func New() Failpoint {
	return Failpoint{}
}

func (Failpoint) Hit(name string) {
	Hit(name)
}
