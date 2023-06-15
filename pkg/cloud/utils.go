/*
Copyright 2018 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cloud

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/GoogleCloudPlatform/k8s-cloud-provider/pkg/cloud/meta"
)

var (
	computePrefix         = "https://www.googleapis.com/compute"
	networkServicesPrefix = "https://www.googleapis.com/networkservices"
	apiGroupRegex         = regexp.MustCompile(`([a-z0-1]*)\/([a-z0-1]*)?\/projects`)
)

// SetAPIDomain sets the root of the URL for the API. The default domain is
// "https://www.googleapis.com".
func SetAPIDomain(domain string) {
	computePrefix = domain + "/compute"
	networkServicesPrefix = domain + "/networkservices"
}

// ResourceID identifies a GCE resource as parsed from compute resource URL.
type ResourceID struct {
	ProjectID string
	Resource  string
	Key       *meta.Key
	// APIGroup identifies the API Group of the resource. If unspecified,
	// "compute" is assumed
	APIGroup meta.APIGroup
}

// Equal returns true if two resource IDs are equal.
func (r *ResourceID) Equal(other *ResourceID) bool {
	switch {
	case r == nil && other == nil:
		return true
	case r == nil || other == nil:
		return false
	case r.ProjectID != other.ProjectID || r.Resource != other.Resource:
		return false
	case r.Key != nil && other.Key != nil:
		return *r.Key == *other.Key
	case r.Key == nil && other.Key == nil:
		return true
	default:
		return false
	}
}

// ResourceMapKey is a flat ResourceID that can be used as a key in maps.
type ResourceMapKey struct {
	APIGroup  meta.APIGroup
	ProjectID string
	Resource  string
	Name      string
	Zone      string
	Region    string
}

func (rk ResourceMapKey) ToID() *ResourceID {
	return &ResourceID{
		APIGroup:  rk.APIGroup,
		ProjectID: rk.ProjectID,
		Resource:  rk.Resource,
		Key:       &meta.Key{Name: rk.Name, Zone: rk.Zone, Region: rk.Region},
	}
}

// MapKey returns a flat key that can be used for referencing in maps.
func (r *ResourceID) MapKey() ResourceMapKey {
	return ResourceMapKey{
		APIGroup:  r.APIGroup,
		ProjectID: r.ProjectID,
		Resource:  r.Resource,
		Name:      r.Key.Name,
		Zone:      r.Key.Zone,
		Region:    r.Key.Region,
	}
}

// RelativeResourceName returns the relative resource name string
// representing this ResourceID.
func (r *ResourceID) RelativeResourceName() string {
	return RelativeResourceName(r.ProjectID, r.Resource, r.Key)
}

// ResourcePath returns the resource path representing this ResourceID.
func (r *ResourceID) ResourcePath() string {
	return ResourcePath(r.Resource, r.Key)
}

func (r *ResourceID) SelfLink(ver meta.Version) string {
	return SelfLink(r.APIGroup, ver, r.ProjectID, r.Resource, r.Key)
}

func (r *ResourceID) String() string {
	prefix := fmt.Sprintf("%s:%s", r.Resource, r.ProjectID)
	if r.APIGroup != "" {
		prefix = fmt.Sprintf("%s/%s", r.APIGroup, prefix)
	}
	switch r.Key.Type() {
	case meta.Zonal:
		return fmt.Sprintf("%s/%s/%s", prefix, r.Key.Zone, r.Key.Name)
	case meta.Regional:
		return fmt.Sprintf("%s/%s/%s", prefix, r.Key.Region, r.Key.Name)
	}
	return fmt.Sprintf("%s/%s", prefix, r.Key.Name)
}

// ParseResourceURL parses resource URLs of the following formats:
//
//	global/<res>/<name>
//	regions/<region>/<res>/<name>
//	zones/<zone>/<res>/<name>
//	projects/<proj>
//	projects/<proj>/global/<res>/<name>
//	projects/<proj>/regions/<region>/<res>/<name>
//	projects/<proj>/zones/<zone>/<res>/<name>
//	[https://www.googleapis.com/compute/<ver>]/projects/<proj>/global/<res>/<name>
//	[https://www.googleapis.com/compute/<ver>]/projects/<proj>/regions/<region>/<res>/<name>
//	[https://www.googleapis.com/compute/<ver>]/projects/<proj>/zones/<zone>/<res>/<name>
//
// Note that ParseResourceURL can't round trip partial paths that do not
// include an API Group.
func ParseResourceURL(url string) (*ResourceID, error) {
	errNotValid := fmt.Errorf("%q is not a valid resource URL", url)

	matches := apiGroupRegex.FindStringSubmatch(url)
	var apiGroup meta.APIGroup
	if len(matches) >= 2 {
		switch matches[1] {
		case "compute":
			apiGroup = meta.APIGroupCompute
		case "networkservices":
			apiGroup = meta.APIGroupNetworkServices
		default:
			return nil, fmt.Errorf("%q does not contain a supported API Group", url)
		}
	}

	// Trim prefix off URL leaving "projects/..."
	projectsIndex := strings.Index(url, "/projects/")
	if projectsIndex >= 0 {
		url = url[projectsIndex+1:]
	}

	parts := strings.Split(url, "/")
	if len(parts) < 2 || len(parts) > 6 {
		return nil, errNotValid
	}

	ret := &ResourceID{APIGroup: apiGroup}
	scopedName := parts
	if parts[0] == "projects" {
		ret.Resource = "projects"
		ret.ProjectID = parts[1]
		scopedName = parts[2:]

		if len(scopedName) == 0 {
			return ret, nil
		}
	}

	switch scopedName[0] {
	case "global":
		if len(scopedName) != 3 {
			return nil, errNotValid
		}
		ret.Resource = scopedName[1]
		ret.Key = meta.GlobalKey(scopedName[2])
		return ret, nil
	case "regions":
		switch len(scopedName) {
		case 2:
			ret.Resource = "regions"
			ret.Key = meta.GlobalKey(scopedName[1])
			return ret, nil
		case 4:
			ret.Resource = scopedName[2]
			ret.Key = meta.RegionalKey(scopedName[3], scopedName[1])
			return ret, nil
		default:
			return nil, errNotValid
		}
	case "zones":
		switch len(scopedName) {
		case 2:
			ret.Resource = "zones"
			ret.Key = meta.GlobalKey(scopedName[1])
			return ret, nil
		case 4:
			ret.Resource = scopedName[2]
			ret.Key = meta.ZonalKey(scopedName[3], scopedName[1])
			return ret, nil
		default:
			return nil, errNotValid
		}
	}
	return nil, errNotValid
}

func copyViaJSON(dest, src interface{}) error {
	bytes, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return json.Unmarshal(bytes, dest)
}

// ResourcePath returns the path starting from the location.
// Example: regions/us-central1/subnetworks/my-subnet
// Deprecated: Use SelfLink instead
func ResourcePath(resource string, key *meta.Key) string {
	switch resource {
	case "zones", "regions":
		return fmt.Sprintf("%s/%s", resource, key.Name)
	case "projects":
		return "invalid-resource"
	}

	switch key.Type() {
	case meta.Zonal:
		return fmt.Sprintf("zones/%s/%s/%s", key.Zone, resource, key.Name)
	case meta.Regional:
		return fmt.Sprintf("regions/%s/%s/%s", key.Region, resource, key.Name)
	case meta.Global:
		return fmt.Sprintf("global/%s/%s", resource, key.Name)
	}
	return "invalid-key-type"
}

// RelativeResourceName returns the path starting from project.
// Example: projects/my-project/regions/us-central1/subnetworks/my-subnet
// Deprecated: Use SelfLink instead
func RelativeResourceName(project, resource string, key *meta.Key) string {
	switch resource {
	case "projects":
		return fmt.Sprintf("projects/%s", project)
	default:
		return fmt.Sprintf("projects/%s/%s", project, ResourcePath(resource, key))
	}
}

// SelfLink returns the self link URL for the given object.
func SelfLink(apiGroup meta.APIGroup, ver meta.Version, project, resource string, key *meta.Key) string {
	var prefix string

	switch apiGroup {
	case meta.APIGroupNetworkServices:
		prefix = networkServicesPrefix
	default:
		prefix = computePrefix
	}

	switch ver {
	case meta.VersionAlpha:
		prefix = prefix + "/alpha"
	case meta.VersionBeta:
		if apiGroup == meta.APIGroupNetworkServices {
			prefix = prefix + "/v1beta1"
		} else {
			prefix = prefix + "/beta"
		}
	case meta.VersionGA:
		prefix = prefix + "/v1"
	default:
		prefix = "invalid-version"
	}

	return fmt.Sprintf("%s/%s", prefix, RelativeResourceName(project, resource, key))
}

// aggregatedListKey return the aggregated list key based on the resource key.
func aggregatedListKey(k *meta.Key) string {
	switch k.Type() {
	case meta.Regional:
		return fmt.Sprintf("regions/%s", k.Region)
	case meta.Zonal:
		return fmt.Sprintf("zones/%s", k.Zone)
	case meta.Global:
		return "global"
	default:
		return "unknownScope"
	}
}
