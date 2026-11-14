package consul

import (
	"github.com/nyakang/nyadb/internal/db/contract"
	"github.com/nyakang/nyadb/internal/db/jsondocument"
)

func buildHTTPAgentTTLTool(kind contract.KVRequestToolKind, input contract.KVRequestToolInput) (contract.KVRequest, error) {
	a := input.HTTPAgentTTL
	input.HTTPAgentTTL = contract.KVHTTPAgentTTLToolInput{}
	if input != (contract.KVRequestToolInput{}) {
		return contract.KVRequest{}, ErrInvalid
	}
	if len(a.ID) > 4096 || len(a.NoteJSON) > requestLimit || len(a.DefinitionJSON) > requestLimit {
		return contract.KVRequest{}, ErrBounds
	}
	var request Request
	var err error
	if kind == contract.KVToolHTTPAgentCheckUpdate {
		if a.IncludeNote || a.NoteJSON != "" {
			return contract.KVRequest{}, ErrInvalid
		}
		request, err = AgentCheckUpdateRequest(a.ID, []byte(a.DefinitionJSON))
	} else {
		if a.DefinitionJSON != "" || !a.IncludeNote && a.NoteJSON != "" {
			return contract.KVRequest{}, ErrInvalid
		}
		var note *string
		if a.IncludeNote {
			if a.NoteJSON == "" {
				note = new(string)
			} else if jsondocument.Decode([]byte(a.NoteJSON), &note, requestLimit) != nil || note == nil {
				return contract.KVRequest{}, ErrInvalid
			}
		}
		switch kind {
		case contract.KVToolHTTPAgentCheckPass:
			request, err = AgentCheckPassRequest(a.ID, note)
		case contract.KVToolHTTPAgentCheckWarn:
			request, err = AgentCheckWarnRequest(a.ID, note)
		case contract.KVToolHTTPAgentCheckFail:
			request, err = AgentCheckFailRequest(a.ID, note)
		default:
			return contract.KVRequest{}, ErrUnsupported
		}
	}
	if err != nil {
		return contract.KVRequest{}, err
	}
	return DocumentRequest(request)
}
