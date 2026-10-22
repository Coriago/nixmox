package domain

type Membership string

const (
	Unjoined Membership = "unjoined"
	Pending  Membership = "pending"
	Full     Membership = "full"
)

type Environment string

const (
	Bootstrap Environment = "bootstrap"
	Installer Environment = "installer"
	Installed Environment = "installed"
)

type Reachability string

const (
	Online            Reachability = "online"
	ReachabilityStale Reachability = "stale"
	Offline           Reachability = "offline"
)

type TaskState string

const (
	Queued      TaskState = "queued"
	Running     TaskState = "running"
	Blocked     TaskState = "blocked"
	Reconciling TaskState = "reconciling"
	Cancelling  TaskState = "cancelling"
	Cancelled   TaskState = "cancelled"
	Succeeded   TaskState = "succeeded"
	Failed      TaskState = "failed"
)

type ActivationMode string

const (
	Switch ActivationMode = "switch"
	Boot   ActivationMode = "boot"
)
