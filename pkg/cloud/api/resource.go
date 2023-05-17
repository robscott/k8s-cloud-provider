/*
Copyright 2023 Google LLC

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

package api

import (
	"fmt"
	"reflect"

	"github.com/GoogleCloudPlatform/k8s-cloud-provider/pkg/cloud"
	"github.com/GoogleCloudPlatform/k8s-cloud-provider/pkg/cloud/meta"
)

// ConversionContext gives which version => version the error occurred on.
type ConversionContext int

const (
	GAToAlphaConversion ConversionContext = iota
	GAToBetaConversion
	AlphaToGAConversion
	AlphaToBetaConversion
	BetaToGAConversion
	BetaToAlphaConversion
	conversionContextCount // Sentinel value used to size arrays.
)

// ConversionError is returned from To*() methods. Inspect this error to get
// more details on what did not convert.
type ConversionError struct {
	// MissingFields is a list of field values that were set but did not
	// translate to the version requested.
	MissingFields []MissingField
}

func (e *ConversionError) hasErr() bool {
	return len(e.MissingFields) > 0
}

// Error implements error.
func (e *ConversionError) Error() string {
	return fmt.Sprintf("ConversionError: missing fields %v", e.MissingFields)
}

// MissingField describes a field that was lost when converting between API
// versions due to the field not being present in struct.
type MissingField struct {
	// Context gives the version to => from.
	Context ConversionContext
	// Path of the field that is missing.
	Path Path
	// Value of the source field.
	Value any
}

type conversionErrors struct {
	missingFields []missingFieldOnCopy
}

// NewResource constructs a new Resource.
//
// If typeTrait is nil, then it will be set to BaseTypeTrait.
func NewResource[GA any, Alpha any, Beta any](
	resourceID *cloud.ResourceID,
	typeTrait TypeTrait[GA, Alpha, Beta],
) *resource[GA, Alpha, Beta] {
	if typeTrait == nil {
		typeTrait = &BaseTypeTrait[GA, Alpha, Beta]{}
	}

	obj := &resource[GA, Alpha, Beta]{
		typeTrait:  typeTrait,
		resourceID: resourceID,
	}

	// Set .Name from the ResourceID.
	setName := func(v reflect.Value) {
		if ft, ok := v.Type().FieldByName("Name"); !ok || ft.Type.Kind() != reflect.String {
			return
		}
		f := v.FieldByName("Name")
		if !f.IsValid() {
			panic(fmt.Sprintf("type does not have .Name (%T)", v.Type()))
		}
		f.Set(reflect.ValueOf(resourceID.Key.Name))
	}
	setName(reflect.ValueOf(&obj.ga).Elem())
	setName(reflect.ValueOf(&obj.alpha).Elem())
	setName(reflect.ValueOf(&obj.beta).Elem())

	return obj
}

// Resource wraps the multi-versioned concrete resources.
type Resource[GA any, Alpha any, Beta any] interface {
	// CheckSchema should be called in init() to ensure that the resource being
	// wrapped meets the assumptions we are making for this the transformations
	// to work.
	CheckSchema() error

	// ResourceID is the resource ID of this resource.
	ResourceID() *cloud.ResourceID

	// ImpliedVersion returns the best API version for the set of
	// fields in the resource. It will return an error if it is not
	// clear which version should be used without missing
	// configuration.
	ImpliedVersion() (meta.Version, error)

	// Access the mutable resource.
	Access(f func(x *GA)) error
	// AccessAlpha resource.
	AccessAlpha(f func(x *Alpha)) error
	// AccessBeta resource.
	AccessBeta(f func(x *Beta)) error

	// ToGA returns the GA version of this resource. Use error.As
	// ConversionError to get the specific details.
	ToGA() (*GA, error)
	// ToAlpha returns the Alpha version of this resource. Use
	// error.As ConversionError to get the specific details.
	ToAlpha() (*Alpha, error)
	// ToBeta returns the Beta version of this resource. Use
	// error.As ConversionError to get the specific details.
	ToBeta() (*Beta, error)

	// Set the value to src. This skips some of the field
	// validation in Access* so should only be used with a valid
	// object returned from GCE.
	Set(src *GA) error
	// SetAlpha the value to src. This skips some of the field
	// validation in Access* so should only be used with a valid
	// object returned from GCE.
	SetAlpha(src *Alpha) error
	// SetBeta the value to src. This skips some of the field
	// validation in Access* so should only be used with a valid
	// object returned from GCE.
	SetBeta(src *Beta) error

	// Freeze the resource to a read-only copy. It is an error if it is ambiguous
	// which version is the correct one i.e. not all fields can be represented in a
	// single version of the resource.
	Freeze() (FrozenResource[GA, Alpha, Beta], error)
}

type resource[GA any, Alpha any, Beta any] struct {
	copierOptions []copierOption
	typeTrait     TypeTrait[GA, Alpha, Beta]

	ga    GA
	alpha Alpha
	beta  Beta

	resourceID *cloud.ResourceID
	errors     [conversionContextCount]conversionErrors
}

func (u *resource[GA, Alpha, Beta]) CheckSchema() error {
	err := checkSchema(reflect.TypeOf(&u.ga))
	if err != nil {
		return err
	}
	err = checkSchema(reflect.TypeOf(&u.alpha))
	if err != nil {
		return err
	}
	err = checkSchema(reflect.TypeOf(&u.beta))
	if err != nil {
		return err
	}
	return nil
}

func (u *resource[GA, Alpha, Beta]) ResourceID() *cloud.ResourceID { return u.resourceID }

const (
	postAccessSkipValidation = 1 << iota
)

func (u *resource[GA, Alpha, Beta]) postAccess(srcVer meta.Version, flags int) error {
	type convert struct {
		dest       reflect.Value
		copyHelper func() error
		errors     *conversionErrors
	}

	var src reflect.Value
	var conversions []convert

	switch srcVer {
	case meta.VersionGA:
		src = reflect.ValueOf(&u.ga)
		conversions = append(conversions, convert{
			dest:       reflect.ValueOf(&u.alpha),
			copyHelper: func() error { return u.typeTrait.CopyHelperGAtoAlpha(&u.alpha, &u.ga) },
			errors:     &u.errors[GAToAlphaConversion],
		})
		conversions = append(conversions, convert{
			dest:       reflect.ValueOf(&u.beta),
			copyHelper: func() error { return u.typeTrait.CopyHelperGAtoBeta(&u.beta, &u.ga) },
			errors:     &u.errors[GAToBetaConversion],
		})
	case meta.VersionAlpha:
		src = reflect.ValueOf(&u.alpha)
		conversions = append(conversions, convert{
			dest:       reflect.ValueOf(&u.ga),
			copyHelper: func() error { return u.typeTrait.CopyHelperAlphaToGA(&u.ga, &u.alpha) },
			errors:     &u.errors[AlphaToGAConversion],
		})
		conversions = append(conversions, convert{
			dest:       reflect.ValueOf(&u.beta),
			copyHelper: func() error { return u.typeTrait.CopyHelperAlphaToBeta(&u.beta, &u.alpha) },
			errors:     &u.errors[AlphaToBetaConversion],
		})
	case meta.VersionBeta:
		src = reflect.ValueOf(&u.beta)
		conversions = append(conversions, convert{
			dest:       reflect.ValueOf(&u.ga),
			copyHelper: func() error { return u.typeTrait.CopyHelperBetaToGA(&u.ga, &u.beta) },
			errors:     &u.errors[BetaToGAConversion],
		})
		conversions = append(conversions, convert{
			dest:       reflect.ValueOf(&u.alpha),
			copyHelper: func() error { return u.typeTrait.CopyHelperBetaToAlpha(&u.alpha, &u.beta) },
			errors:     &u.errors[BetaToAlphaConversion],
		})
	}

	if flags&postAccessSkipValidation == 0 {
		if err := checkPostAccess(u.typeTrait.FieldTraits(srcVer), src); err != nil {
			return err
		}
	}
	for _, conv := range conversions {
		c := newCopier(u.copierOptions...)
		if err := c.do(conv.dest, src); err != nil {
			return err
		}
		if err := conv.copyHelper(); err != nil {
			return err
		}
		conv.errors.missingFields = c.missing
	}

	return nil
}

func (u *resource[GA, Alpha, Beta]) Access(f func(x *GA)) error {
	f(&u.ga)
	return u.postAccess(meta.VersionGA, 0)
}

func (u *resource[GA, Alpha, Beta]) AccessAlpha(f func(x *Alpha)) error {
	f(&u.alpha)
	return u.postAccess(meta.VersionAlpha, 0)
}

func (u *resource[GA, Alpha, Beta]) AccessBeta(f func(x *Beta)) error {
	f(&u.beta)
	return u.postAccess(meta.VersionBeta, 0)
}

func (u *resource[GA, Alpha, Beta]) ImpliedVersion() (meta.Version, error) {
	_, gaErr := u.ToGA()
	_, alphaErr := u.ToAlpha()
	_, betaErr := u.ToBeta()

	switch {
	case gaErr == nil && alphaErr == nil && betaErr == nil:
		return meta.VersionGA, nil
	case gaErr != nil && alphaErr == nil && betaErr != nil:
		return meta.VersionAlpha, nil
	case gaErr != nil && alphaErr != nil && betaErr == nil:
		return meta.VersionBeta, nil
	default:
		return meta.VersionGA, fmt.Errorf("indeterminant version (ga=%v, alpha=%v, beta=%v)", gaErr, alphaErr, betaErr)
	}
}

func (u *resource[GA, Alpha, Beta]) ToGA() (*GA, error) {
	var errs ConversionError
	for _, cc := range []ConversionContext{AlphaToGAConversion, BetaToGAConversion} {
		for _, mf := range u.errors[cc].missingFields {
			errs.MissingFields = append(errs.MissingFields, MissingField{
				Context: cc,
				Path:    mf.Path,
				Value:   mf.Value,
			})
		}
	}
	if errs.hasErr() {
		return &u.ga, &errs
	}
	return &u.ga, nil
}

func (u *resource[GA, Alpha, Beta]) ToAlpha() (*Alpha, error) {
	var errs ConversionError
	for _, cc := range []ConversionContext{GAToAlphaConversion, BetaToAlphaConversion} {
		for _, mf := range u.errors[cc].missingFields {
			errs.MissingFields = append(errs.MissingFields, MissingField{
				Context: cc,
				Path:    mf.Path,
				Value:   mf.Value,
			})
		}
	}
	if errs.hasErr() {
		return &u.alpha, &errs
	}
	return &u.alpha, nil
}

func (u *resource[GA, Alpha, Beta]) ToBeta() (*Beta, error) {
	var errs ConversionError
	for _, cc := range []ConversionContext{GAToBetaConversion, AlphaToBetaConversion} {
		for _, mf := range u.errors[cc].missingFields {
			errs.MissingFields = append(errs.MissingFields, MissingField{
				Context: cc,
				Path:    mf.Path,
				Value:   mf.Value,
			})
		}
	}
	if errs.hasErr() {
		return &u.beta, &errs
	}
	return &u.beta, nil
}

// TODO: Set semantics need to be reworked. The copy over to the other versions
// should skip Access validation. Don't use this for the time being.

func (u *resource[GA, Alpha, Beta]) Set(src *GA) error {
	c := newCopier(u.copierOptions...)
	if err := c.do(reflect.ValueOf(&u.ga), reflect.ValueOf(src)); err != nil {
		return err
	}
	return u.postAccess(meta.VersionGA, postAccessSkipValidation)
}

func (u *resource[GA, Alpha, Beta]) SetAlpha(src *Alpha) error {
	c := newCopier(u.copierOptions...)
	if err := c.do(reflect.ValueOf(&u.alpha), reflect.ValueOf(src)); err != nil {
		return err
	}
	return u.postAccess(meta.VersionAlpha, postAccessSkipValidation)
}

func (u *resource[GA, Alpha, Beta]) SetBeta(src *Beta) error {
	c := newCopier(u.copierOptions...)
	if err := c.do(reflect.ValueOf(&u.beta), reflect.ValueOf(src)); err != nil {
		return err
	}
	return u.postAccess(meta.VersionBeta, postAccessSkipValidation)
}

func (u *resource[GA, Alpha, Beta]) Freeze() (FrozenResource[GA, Alpha, Beta], error) {
	ver, err := u.ImpliedVersion()
	if err != nil {
		return nil, err
	}
	// For the structures in the other versions, fill in
	// zero-valued fields in the metafields. This ensures that if
	// the resource can be diff'd and sync'd correctly in all
	// versions.
	//
	// Example:
	//
	// - Beta has an extra field "*Feature1" that is not in GA.
	// - We determine that the version stored on the server is the
	//   Beta version, so we do a diff with beta structs -- which
	//   results in a diff and update.
	// - At this point, we need to set NullFields = ["Feature1"],
	//   otherwise the update will ignore the field.
	if ver != meta.VersionGA {
		if err := fillNullAndForceSend(u.typeTrait.FieldTraits(meta.VersionGA), reflect.ValueOf(&u.ga)); err != nil {
			return nil, err
		}
	}
	if ver != meta.VersionAlpha {
		if err := fillNullAndForceSend(u.typeTrait.FieldTraits(meta.VersionAlpha), reflect.ValueOf(&u.alpha)); err != nil {
			return nil, err
		}
	}
	if ver != meta.VersionBeta {
		if err := fillNullAndForceSend(u.typeTrait.FieldTraits(meta.VersionBeta), reflect.ValueOf(&u.beta)); err != nil {
			return nil, err
		}
	}

	return &frozenResource[GA, Alpha, Beta]{x: u, ver: ver}, nil
}
