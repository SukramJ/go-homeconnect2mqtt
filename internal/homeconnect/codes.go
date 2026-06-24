// SPDX-License-Identifier: MIT
// Copyright (C) 2026 SukramJ

package homeconnect

import "fmt"

// responseCodes is the full error-code table from const.py
// (docs/01-protocol.md §9). A non-nil code on a RESPONSE means failure.
var responseCodes = map[int]string{
	200: "OK",
	202: "Accepted",
	400: "BadRequest",
	403: "Forbidden",
	404: "NotFound",
	405: "MethodNotAllowed",
	413: "RequestEntityTooLong",
	414: "RequestUriTooLong",
	429: "TooManyRequests",
	500: "InternalServerError",
	501: "NotImplemented",
	502: "BadGateway",
	503: "ServiceUnavailable",
	504: "GatewayTimeout",
	507: "InsufficientMemory",
	512: "UnknownUID",
	513: "WriteRequestUnknownUID",
	514: "ReadRequestUnknownUID",
	515: "Busy",
	516: "WriteRequestBusy",
	517: "ReadRequestBusy",
	518: "NoAccess",
	519: "WriteRequestNoAccess",
	520: "ReadRequestNoAccess",
	521: "NoAccessByList",
	522: "WriteRequestNoAccessByList",
	523: "ReadRequestNoAccessByList",
	524: "NotAvailable",
	525: "WriteRequestNotAvailable",
	526: "ReadRequestNotAvailable",
	527: "NotAvailableByList",
	528: "WriteRequestNotAvailableByList",
	529: "ReadRequestNotAvailableByList",
	530: "NoExecution",
	531: "ValueOutOfRange",
	532: "InvalidUIDValue",
	533: "Incomplete",
	534: "Inconsistent",
	535: "CmdViolation",
	536: "InvalidFormat",
	537: "RemoteControlNotActive",
	538: "RemoteStartNotActive",
	539: "LockedByLocalControl",
	540: "DeviceStateNotCompliant",
	541: "ProcessStateNotCompliant",
	542: "BackendNotConnected",
	543: "EnergyManagementNotConnected",
	544: "NotInLocalWiFi",
}

// CodeName returns the symbolic name for a response code, or "Unknown".
func CodeName(code int) string {
	if name, ok := responseCodes[code]; ok {
		return name
	}
	return "Unknown"
}

// CodeResponseError is returned when an appliance answers a request with a
// non-nil error code (docs/01-protocol.md §6.5/§9).
type CodeResponseError struct {
	Code     int
	Resource string
}

// Error implements error.
func (e *CodeResponseError) Error() string {
	return fmt.Sprintf("homeconnect: %s -> code %d (%s)", e.Resource, e.Code, CodeName(e.Code))
}

// codeError builds a *CodeResponseError from a message, or nil when the
// response carries no error code.
func codeError(m *Message) error {
	if m.Code == nil {
		return nil
	}
	return &CodeResponseError{Code: *m.Code, Resource: m.Resource}
}
