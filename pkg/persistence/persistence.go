package persistence

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/betbot/gobet/pkg/logger"
)

// Service 持久化服务接口
type Service interface {
	NewStore(prefix, id, tag string) Store
}

// Store 存储接口
type Store interface {
	Save(data interface{}) error
	Load(data interface{}) error
}

// ErrNotExists 表示数据不存在
var ErrNotExists = fmt.Errorf("persistence data not exists")

// JSONFileService 基于 JSON 文件的持久化服务
type JSONFileService struct {
	baseDir string
}

// NewJSONFileService 创建 JSON 文件持久化服务
func NewJSONFileService(baseDir string) *JSONFileService {
	return &JSONFileService{
		baseDir: baseDir,
	}
}

// NewStore 创建新的存储
func (s *JSONFileService) NewStore(prefix, id, tag string) Store {
	key := fmt.Sprintf("%s:%s:%s", prefix, id, tag)
	return &JSONFileStore{
		service: s,
		key:     key,
	}
}

// JSONFileStore JSON 文件存储实现
type JSONFileStore struct {
	service *JSONFileService
	key     string
}

// Save 保存数据
func (s *JSONFileStore) Save(data interface{}) error {
	// TODO: 实现文件保存逻辑
	logger.Debugf("[persistence] Save: key=%s", s.key)
	return nil
}

// Load 加载数据
func (s *JSONFileStore) Load(data interface{}) error {
	// TODO: 实现文件加载逻辑
	logger.Debugf("[persistence] Load: key=%s", s.key)
	return ErrNotExists
}

// LoadFields 加载带 persistence tag 的字段
func LoadFields(obj interface{}, id string, service Service) error {
	return iterateFieldsByTag(obj, "persistence", true, func(
		tag string, field reflect.StructField, value reflect.Value,
	) error {
		logger.Debugf("[LoadFields] loading field %s, tag=%s", field.Name, tag)

		// 创建新值
		newValueInf := newTypeValueInterface(value.Type())

		// 加载数据
		store := service.NewStore("state", id, tag)
		if err := store.Load(&newValueInf); err != nil {
			if err == ErrNotExists {
				logger.Debugf("[LoadFields] state key does not exist, id=%s, tag=%s", id, tag)
				return nil
			}
			return err
		}

		// 设置值
		newValue := reflect.ValueOf(newValueInf)
		if value.Kind() != reflect.Ptr && newValue.Kind() == reflect.Ptr {
			newValue = newValue.Elem()
		}

		logger.Debugf("[LoadFields] %s = %v -> %v", field.Name, value, newValue)
		value.Set(newValue)
		return nil
	})
}

// SaveFields 保存带 persistence tag 的字段
func SaveFields(obj interface{}, id string, service Service) error {
	return iterateFieldsByTag(obj, "persistence", true, func(
		tag string, ft reflect.StructField, fv reflect.Value,
	) error {
		logger.Debugf("[SaveFields] storing field %s, tag=%s", ft.Name, tag)

		inf := fv.Interface()
		store := service.NewStore("state", id, tag)
		return store.Save(inf)
	})
}

// iterateFieldsByTag 遍历结构体字段，查找指定 tag
func iterateFieldsByTag(obj interface{}, tagName string, includeNested bool, fn func(tag string, field reflect.StructField, value reflect.Value) error) error {
	v := reflect.ValueOf(obj)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}

	if v.Kind() != reflect.Struct {
		return fmt.Errorf("object must be a struct or pointer to struct")
	}

	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		value := v.Field(i)

		// 跳过未导出的字段
		if !value.CanSet() {
			continue
		}

		// 检查 tag
		tag := field.Tag.Get(tagName)
		if tag == "" || tag == "-" {
			if includeNested && value.Kind() == reflect.Struct {
				// 递归处理嵌套结构
				if err := iterateFieldsByTag(value.Addr().Interface(), tagName, includeNested, fn); err != nil {
					return err
				}
			}
			continue
		}

		// 处理 tag 值（可能包含选项，如 "tag,option"）
		tagParts := strings.Split(tag, ",")
		tagValue := tagParts[0]

		// 调用回调函数
		if err := fn(tagValue, field, value); err != nil {
			return err
		}
	}

	return nil
}

// newTypeValueInterface 创建指定类型的新值
func newTypeValueInterface(typ reflect.Type) interface{} {
	if typ.Kind() == reflect.Ptr {
		return reflect.New(typ.Elem()).Interface()
	}
	return reflect.New(typ).Interface()
}

