/*
Copyright (C) 2022-2024 ApeCloud Co., Ltd

This file is part of KubeBlocks project

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program.  If not, see <http://www.gnu.org/licenses/>.
*/

package core

import (
	"encoding/json"
	"path"
	"slices"
	"strings"

	"github.com/StudioSol/set"
	"github.com/spf13/cast"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	"github.com/apecloud/kubeblocks/pkg/configuration/util"
	"github.com/apecloud/kubeblocks/pkg/unstructured"
)

type ConfigLoaderProvider func(option CfgOption) (*cfgWrapper, error)

// ReconfiguringProgress defines the progress percentage.
// range: 0~100
// Unconfirmed(-1) describes an uncertain progress, e.g: fsm is failed.
// +enum
type ReconfiguringProgress int32

type PolicyExecStatus struct {
	PolicyName string
	ExecStatus string
	Status     string

	SucceedCount  int32
	ExpectedCount int32
}

const (
	Unconfirmed int32 = -1
	NotStarted  int32 = 0
)

const emptyJSON = "{}"

var (
	loaderProvider = map[ConfigType]ConfigLoaderProvider{}
)

func init() {
	// For RAW
	loaderProvider[CfgRawType] = func(option CfgOption) (*cfgWrapper, error) {
		if len(option.RawData) == 0 {
			return nil, MakeError("rawdata not empty! [%v]", option)
		}

		meta := cfgWrapper{
			name:      "raw",
			fileCount: 0,
			v:         make([]unstructured.ConfigObject, 1),
			indexer:   make(map[string]unstructured.ConfigObject, 1),
		}

		v, err := unstructured.LoadConfig(meta.name, string(option.RawData), option.CfgType)
		if err != nil {
			option.Log.Error(err, "failed to parse config!", "context", option.RawData)
			return nil, err
		}

		meta.v[0] = v
		meta.indexer[meta.name] = v
		return &meta, nil
	}

	// For CM/TPL
	loaderProvider[CfgCmType] = func(option CfgOption) (*cfgWrapper, error) {
		if option.ConfigResource == nil {
			return nil, MakeError("invalid k8s resource[%v]", option)
		}

		ctx := option.ConfigResource
		if ctx.ConfigData == nil && ctx.ResourceReader != nil {
			configs, err := ctx.ResourceReader(ctx.CfgKey)
			if err != nil {
				return nil, WrapError(err, "failed to get cm, cm key: [%v]", ctx.CfgKey)
			}
			ctx.ConfigData = configs
		}

		fileCount := len(ctx.ConfigData)
		meta := cfgWrapper{
			name:      path.Base(ctx.CfgKey.Name),
			fileCount: fileCount,
			v:         make([]unstructured.ConfigObject, fileCount),
			indexer:   make(map[string]unstructured.ConfigObject, 1),
		}

		var err error
		var index = 0
		var v unstructured.ConfigObject
		for fileName, content := range ctx.ConfigData {
			if ctx.CMKeys != nil && !ctx.CMKeys.InArray(fileName) {
				continue
			}
			if v, err = unstructured.LoadConfig(fileName, content, option.CfgType); err != nil {
				return nil, WrapError(err, "failed to load config: filename[%s], type[%s]", fileName, option.CfgType)
			}
			meta.indexer[fileName] = v
			meta.v[index] = v
			index++
		}
		return &meta, nil
	}

	// For TPL
	loaderProvider[CfgTplType] = loaderProvider[CfgCmType]
}

type cfgWrapper struct {
	// name is config name
	name string
	// volumeName string

	// fileCount
	fileCount int
	// indexer   map[string]*viper.Viper
	indexer map[string]unstructured.ConfigObject
	v       []unstructured.ConfigObject
}

type dataConfig struct {
	// Option is config for
	Option CfgOption

	// cfgWrapper references configuration template or configmap
	*cfgWrapper
}

func NewConfigLoader(option CfgOption) (*dataConfig, error) {
	loader, ok := loaderProvider[option.Type]
	if !ok {
		return nil, MakeError("not supported config type: %s", option.Type)
	}

	meta, err := loader(option)
	if err != nil {
		return nil, err
	}

	return &dataConfig{
		Option:     option,
		cfgWrapper: meta,
	}, nil
}

// Option for operator
type Option func(ctx *CfgOpOption)

func (c *cfgWrapper) MergeFrom(params map[string]interface{}, option CfgOpOption) error {
	var err error
	var cfg unstructured.ConfigObject

	if cfg = c.getConfigObject(option); cfg == nil {
		return MakeError("not found the config file:[%s]", option.FileName)
	}
	for paramKey, paramValue := range params {
		if paramValue != nil {
			err = cfg.Update(c.generateKey(paramKey, option), paramValue)
		} else {
			err = cfg.RemoveKey(c.generateKey(paramKey, option))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *cfgWrapper) ToCfgContent() (map[string]string, error) {
	fileContents := make(map[string]string, c.fileCount)
	for fileName, v := range c.indexer {
		content, err := v.Marshal()
		if err != nil {
			return nil, err
		}
		fileContents[fileName] = content
	}
	return fileContents, nil
}

type ConfigPatchInfo struct {
	IsModify bool
	// new config
	AddConfig map[string]interface{}

	// delete config
	DeleteConfig map[string]interface{}

	// update config
	// patch json
	UpdateConfig map[string][]byte

	Target      *cfgWrapper
	LastVersion *cfgWrapper
}

func NewCfgOptions(filename string, options ...Option) CfgOpOption {
	context := CfgOpOption{
		FileName: filename,
	}

	for _, op := range options {
		op(&context)
	}

	return context
}

func WithFormatterConfig(formatConfig *appsv1alpha1.FormatterConfig) Option {
	return func(ctx *CfgOpOption) {
		if formatConfig.Format == appsv1alpha1.Ini && formatConfig.IniConfig != nil {
			ctx.IniContext = &IniContext{
				SectionName:              formatConfig.IniConfig.SectionName,
				ParametersInSectionAsMap: formatConfig.IniConfig.ParametersInSectionAsMap,
				ApplyAllSection:          false,
			}
			if formatConfig.IniConfig.ApplyAllSection != nil {
				ctx.IniContext.ApplyAllSection = *formatConfig.IniConfig.ApplyAllSection
			}
		}
	}
}

func NestedPrefixField(formatConfig *appsv1alpha1.FormatterConfig) string {
	if formatConfig != nil && formatConfig.Format == appsv1alpha1.Ini && formatConfig.IniConfig != nil {
		if !formatConfig.IniConfig.IsSupportMultiSection() {
			return formatConfig.IniConfig.SectionName
		}
	}
	return ""
}

func (c *cfgWrapper) Query(jsonpath string, option CfgOpOption) ([]byte, error) {
	if option.AllSearch && c.fileCount > 1 {
		return c.queryAllCfg(jsonpath, option)
	}

	cfg := c.getConfigObject(option)
	if cfg == nil {
		return nil, MakeError("not found the config file:[%s]", option.FileName)
	}

	iniContext := option.IniContext
	if iniContext != nil && len(iniContext.SectionName) > 0 {
		cfg = cfg.SubConfig(iniContext.SectionName)
		if cfg == nil {
			return nil, MakeError("the section[%s] does not exist in the config file", iniContext.SectionName)
		}
	}

	return util.RetrievalWithJSONPath(cfg.GetAllParameters(), jsonpath)
}

func (c *cfgWrapper) queryAllCfg(jsonpath string, option CfgOpOption) ([]byte, error) {
	tops := make(map[string]interface{}, c.fileCount)

	for filename, v := range c.indexer {
		tops[filename] = v.GetAllParameters()
	}
	return util.RetrievalWithJSONPath(tops, jsonpath)
}

func (c *cfgWrapper) getConfigObject(option CfgOpOption) unstructured.ConfigObject {
	if len(c.v) == 0 {
		return nil
	}

	if len(option.FileName) == 0 {
		return c.v[0]
	} else {
		return c.indexer[option.FileName]
	}
}

func (c *cfgWrapper) generateKey(paramKey string, option CfgOpOption) string {
	if option.IniContext != nil {
		// support special section, e.g: mysql.default-character-set
		if strings.Index(paramKey, unstructured.DelimiterDot) > 0 {
			return paramKey
		}
		sectionName := fromIniConfig(paramKey, option.IniContext)
		if sectionName != "" {
			return strings.Join([]string{sectionName, paramKey}, unstructured.DelimiterDot)
		}
	}
	return paramKey
}

func fromIniConfig(paramKey string, iniContext *IniContext) string {
	for s, params := range iniContext.ParametersInSectionAsMap {
		if slices.Contains(params, paramKey) {
			return s
		}
	}
	return iniContext.SectionName
}

func FromCMKeysSelector(keys []string) *set.LinkedHashSetString {
	var cmKeySet *set.LinkedHashSetString
	if len(keys) > 0 {
		cmKeySet = set.NewLinkedHashSetString(keys...)
	}
	return cmKeySet
}

func GenerateVisualizedParamsList(configPatch *ConfigPatchInfo, formatConfig *appsv1alpha1.FormatterConfig, sets *set.LinkedHashSetString) []VisualizedParam {
	return GenerateVisualizedParamsListImpl(configPatch, formatConfig, sets, false)
}

// GenerateVisualizedParamsListImpl Generate visualized parameters list
// for kbcli edit-config command
func GenerateVisualizedParamsListImpl(configPatch *ConfigPatchInfo, formatConfig *appsv1alpha1.FormatterConfig, sets *set.LinkedHashSetString, force bool) []VisualizedParam {
	if !configPatch.IsModify {
		return nil
	}

	var trimPrefix = NestedPrefixField(formatConfig)
	var applyAllSections = isIniCfgAndSupportMultiSection(formatConfig) || force
	var section = getIniSection(formatConfig)

	r := make([]VisualizedParam, 0)
	r = append(r, generateUpdateParam(configPatch.UpdateConfig, trimPrefix, sets, applyAllSections, section)...)
	r = append(r, generateUpdateKeyParam(configPatch.AddConfig, trimPrefix, AddedType, sets, applyAllSections, section)...)
	r = append(r, generateUpdateKeyParam(configPatch.DeleteConfig, trimPrefix, DeletedType, sets, applyAllSections, section)...)
	return r
}

func getIniSection(config *appsv1alpha1.FormatterConfig) string {
	if config != nil && config.IniConfig != nil {
		return config.IniConfig.SectionName
	}
	return ""
}

func generateUpdateParam(updatedParams map[string][]byte, trimPrefix string, sets *set.LinkedHashSetString, applyAllSections bool, defaultSection string) []VisualizedParam {
	r := make([]VisualizedParam, 0, len(updatedParams))

	for key, b := range updatedParams {
		// TODO support keys
		if sets != nil && sets.Length() > 0 && !sets.InArray(key) {
			continue
		}
		var v any
		if err := json.Unmarshal(b, &v); err != nil {
			return nil
		}
		if params := checkAndFlattenMap(v, trimPrefix, applyAllSections, defaultSection); params != nil {
			r = append(r, VisualizedParam{
				Key:        key,
				Parameters: params,
				UpdateType: UpdatedType,
			})
		}
	}
	return r
}

func checkAndFlattenMap(v any, trim string, applyAllSections bool, defaultSection string) []ParameterPair {
	m := cast.ToStringMap(v)
	if m != nil && !applyAllSections && trim != "" {
		m = cast.ToStringMap(m[trim])
	}
	if m != nil {
		return flattenMap(m, "", applyAllSections, defaultSection)
	}
	return nil
}

func flattenMap(m map[string]interface{}, prefix string, applyAllSections bool, defaultSection string) []ParameterPair {
	if prefix != "" {
		prefix += unstructured.DelimiterDot
	}

	r := make([]ParameterPair, 0)
	for k, val := range m {
		fullKey := prefix + k
		switch m2 := val.(type) {
		case map[string]interface{}:
			r = append(r, flattenMap(m2, fullKey, applyAllSections, defaultSection)...)
		case []interface{}:
			r = append(r, ParameterPair{
				Key:   transArrayFieldName(trimPrimaryKeyName(fullKey, applyAllSections, defaultSection)),
				Value: util.ToPointer(transJSONString(val)),
			})
		default:
			var v *string = nil
			if val != nil {
				v = util.ToPointer(cast.ToString(val))
			}
			r = append(r, ParameterPair{
				Key:   trimPrimaryKeyName(fullKey, applyAllSections, defaultSection),
				Value: v,
			})
		}
	}
	return r
}

func trimPrimaryKeyName(key string, applyAllSections bool, defaultSection string) string {
	if !applyAllSections || defaultSection == "" {
		return key
	}

	pos := strings.Index(key, ".")
	switch {
	case pos < 0:
		return key
	case key[:pos] == defaultSection:
		return key[pos+1:]
	default:
		return key
	}
}

func generateUpdateKeyParam(
	files map[string]interface{},
	trimPrefix string,
	updatedType ParameterUpdateType,
	sets *set.LinkedHashSetString,
	applyAllSections bool,
	defaultSection string) []VisualizedParam {
	r := make([]VisualizedParam, 0, len(files))

	for key, params := range files {
		if sets != nil && sets.Length() > 0 && !sets.InArray(key) {
			continue
		}
		if params := checkAndFlattenMap(params, trimPrefix, applyAllSections, defaultSection); params != nil {
			r = append(r, VisualizedParam{
				Key:        key,
				Parameters: params,
				UpdateType: updatedType,
			})
		}
	}
	return r
}

func transJSONString(val interface{}) string {
	if val == nil {
		return ""
	}
	b, _ := json.Marshal(val)
	return string(b)
}

func fromJSONString(val *string) any {
	if val == nil {
		return nil
	}
	if *val == "" {
		return []any{}
	}
	var v any
	_ = json.Unmarshal([]byte(*val), &v)
	return v
}

const ArrayFieldPrefix = "@"

func transArrayFieldName(key string) string {
	return ArrayFieldPrefix + key
}

func hasArrayField(key string) bool {
	return strings.HasPrefix(key, ArrayFieldPrefix)
}

func GetValidFieldName(key string) string {
	if hasArrayField(key) {
		return strings.TrimPrefix(key, ArrayFieldPrefix)
	}
	return key
}
