// Code generated by pegomock. DO NOT EDIT.
package matchers

import (
	"github.com/petergtz/pegomock"
	"reflect"

	models "github.com/runatlantis/atlantis/server/events/models"
)

func AnySliceOfPtrToModelsProjectCommandContext() []*models.ProjectCommandContext {
	pegomock.RegisterMatcher(pegomock.NewAnyMatcher(reflect.TypeOf((*([]*models.ProjectCommandContext))(nil)).Elem()))
	var nullValue []*models.ProjectCommandContext
	return nullValue
}

func EqSliceOfPtrToModelsProjectCommandContext(value []*models.ProjectCommandContext) []*models.ProjectCommandContext {
	pegomock.RegisterMatcher(&pegomock.EqMatcher{Value: value})
	var nullValue []*models.ProjectCommandContext
	return nullValue
}

func NotEqSliceOfPtrToModelsProjectCommandContext(value []*models.ProjectCommandContext) []*models.ProjectCommandContext {
	pegomock.RegisterMatcher(&pegomock.NotEqMatcher{Value: value})
	var nullValue []*models.ProjectCommandContext
	return nullValue
}

func SliceOfPtrToModelsProjectCommandContextThat(matcher pegomock.ArgumentMatcher) []*models.ProjectCommandContext {
	pegomock.RegisterMatcher(matcher)
	var nullValue []*models.ProjectCommandContext
	return nullValue
}
