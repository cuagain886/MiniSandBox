package docker

type InjectedArtifact struct {
	Name string
	Data []byte
	Mode int64
}
