package port

type Failpoint interface {
	Hit(name string)
}
