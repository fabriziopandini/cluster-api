/*
Copyright 2022 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package tkr

import (
	"fmt"
)

func ResolveFor(version string) Info {
	return Info{
		TkrId:        fmt.Sprintf("%s.vmware.0", version),
		OsImage:      "ubuntu",
		MachineImage: fmt.Sprintf("kindest/node:%s", version),
	}
}

type Info struct {
	TkrId        string
	OsImage      string
	MachineImage string
}

func (tkr *Info) SetTopologyAnnotations(annotations map[string]string) {
	// annotations to be used in patches, MUST NOT change by version otherwise a rollout is triggered
	annotations["tkr-os"] = tkr.OsImage
}
