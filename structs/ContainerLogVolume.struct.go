package structs

// ContainerLogVolume is how much one container stored in container_logs on one
// UTC day (APL-120): the report for finding the containers that fill the
// control-plane disk.
type ContainerLogVolume struct {
	Day           string  `json:"day"` // YYYY-MM-DD, UTC
	ContainerID   *int    `json:"container_id"`
	ContainerName *string `json:"container_name"`
	StackID       *int    `json:"stack_id"`
	Lines         int64   `json:"lines"`
	Bytes         int64   `json:"bytes"` // message bytes, not on-disk size (indexes and row overhead add more)
}
