package utils

import (
	"encoding/json"
)

const (
	JOIN_REQUEST        = "join_reqeust"
	FILE_PULL           = "file_pull"
	SUSPEND_REQUEST     = "suspend_request"
	SUSPEND_RESPONSE    = "suspend_response"
	SNAPSHOT_REQUEST    = "snapshot_request"
	SNAPSHOT_RESPONSE   = "snapshot_response"
	RESTORE_REQUEST     = "restore_request"
	TOPO_SUBMISSION     = "topo_submission"
	TOPO_SUBMISSION_RES = "topo_submission_response"
	BOLT_TASK           = "bolt_task"
	SPOUT_TASK          = "spout_task"
	TASK_ALL_DISPATCHED = "task_all_dispatched"
	CONN_NOTIFY         = "conn_notify"
	GROUPING_BY_FIELD   = "grouping_by_field"
	GROUPING_BY_SHUFFLE = "grouping_by_shuffle"
	GROUPING_BY_ALL     = "grouping_by_all"

	CONTRACTOR_BASE_PORT = 6000
	DRIVER_PORT          = 5050
)

type PayloadHeader struct {
	Type string
}

type PayloadMessage struct {
	Header  PayloadHeader
	Content []byte
}

type JoinRequest struct {
	Name string
}

type FilePull struct {
	Filename string
}

type BoltTaskMessage struct {
	Name                 string
	Port                 string
	PrevBoltAddr         []string
	PrevBoltGroupingHint string
	PrevBoltFieldIndex   int
	SuccBoltGroupingHint string
	SuccBoltFieldIndex   int
	PluginFile           string
	PluginSymbol         string
	SnapshotVersion      int
}

type SpoutTaskMessage struct {
	Name            string
	Port            string
	GroupingHint    string
	FieldIndex      int
	PluginFile      string
	PluginSymbol    string
	SnapshotVersion int
}

func Marshal(contentType string, content interface{}) ([]byte, error) {
	contentBytes, err := json.Marshal(content)
	if err != nil {
		return nil, err
	}

	msg := PayloadMessage{
		PayloadHeader{Type: contentType},
		contentBytes,
	}

	return json.Marshal(msg)
}

func CheckType(raw []byte) *PayloadMessage {
	payload := &PayloadMessage{}
	json.Unmarshal(raw, payload)
	return payload
}

func Unmarshal(raw []byte, content interface{}) {
	json.Unmarshal(raw, content)
}
