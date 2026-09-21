package port

type ReferenceMetrics interface {
	ReferenceRetried()
	ReferenceFailed()
	ReferencePending(count int64)
}
