package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	apiv1 "k8s.io/api/core/v1"
	apierr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	wfv1 "github.com/argoproj/argo/v3/pkg/apis/workflow/v1alpha1"
)

type configMapCache struct {
	namespace  string
	name       string
	kubeClient kubernetes.Interface
	lock       sync.RWMutex
}

func NewConfigMapCache(ns string, ki kubernetes.Interface, n string) MemoizationCache {
	return &configMapCache{
		namespace:  ns,
		name:       n,
		kubeClient: ki,
		lock:       sync.RWMutex{},
	}
}

func (c *configMapCache) logError(err error, fields log.Fields, message string) {
	log.WithFields(log.Fields{"namespace": c.namespace, "name": c.name}).WithFields(fields).WithError(err).Debug(message)
}

func (c *configMapCache) logInfo(fields log.Fields, message string) {
	log.WithFields(log.Fields{"namespace": c.namespace, "name": c.name}).WithFields(fields).Info(message)
}

func (c *configMapCache) Load(ctx context.Context, key string) (*Entry, error) {
	if !cacheKeyRegex.MatchString(key) {
		return nil, fmt.Errorf("invalid cache key: %s", key)
	}

	c.lock.Lock()
	defer c.lock.Unlock()

	cm, err := c.kubeClient.CoreV1().ConfigMaps(c.namespace).Get(ctx, c.name, metav1.GetOptions{})
	if err != nil {
		if apierr.IsNotFound(err) {
			c.logError(err, log.Fields{}, "config map cache miss: config map does not exist")
			return nil, nil
		}
		c.logError(err, log.Fields{}, "Error loading config map cache")
		return nil, fmt.Errorf("could not load config map cache: %w", err)
	}

	c.logInfo(log.Fields{}, "config map cache loaded")

	rawEntry, ok := cm.Data[key]
	if !ok || rawEntry == "" {
		c.logInfo(log.Fields{}, "config map cache miss: entry does not exist")
		return nil, nil
	}

	var entry Entry
	err = json.Unmarshal([]byte(rawEntry), &entry)
	if err != nil {
		return nil, fmt.Errorf("malformed cache entry: could not unmarshal JSON; unable to parse: %w", err)
	}

	return &entry, nil
}

func (c *configMapCache) Save(ctx context.Context, key string, nodeId string, value *wfv1.Outputs) error {
	if !cacheKeyRegex.MatchString(key) {
		errString := fmt.Sprintf("invalid cache key: %s", key)
		err := errors.New(errString)
		c.logError(err, log.Fields{"key": key}, errString)
		return err
	}

	c.lock.Lock()
	defer c.lock.Unlock()

	c.logInfo(log.Fields{"key": key, "nodeId": nodeId}, "Saving ConfigMap cache entry")

	cache, err := c.kubeClient.CoreV1().ConfigMaps(c.namespace).Get(ctx, c.name, metav1.GetOptions{})
	if apierr.IsNotFound(err) || cache == nil {
		cache, err = c.kubeClient.CoreV1().ConfigMaps(c.namespace).Create(ctx, &apiv1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: c.name,
			},
		}, metav1.CreateOptions{})
		if err != nil {
			c.logError(err, log.Fields{"key": key, "nodeId": nodeId}, "Error saving to ConfigMap cache")
			return fmt.Errorf("could not save to config map cache: %w", err)
		}
	}

	newEntry := Entry{
		NodeID:            nodeId,
		Outputs:           value,
		CreationTimestamp: metav1.Time{Time: time.Now()},
	}

	entryJSON, err := json.Marshal(newEntry)
	if err != nil {
		c.logError(err, log.Fields{"key": key, "nodeId": nodeId}, "Unable to marshal cache entry")
		return fmt.Errorf("unable to marshal cache entry: %w", err)
	}

	if cache.Data == nil {
		cache.Data = make(map[string]string)
	}
	cache.Data[key] = string(entryJSON)

	_, err = c.kubeClient.CoreV1().ConfigMaps(c.namespace).Update(ctx, cache, metav1.UpdateOptions{})
	if err != nil {
		c.logError(err, log.Fields{"key": key, "nodeId": nodeId}, "Kubernetes error creating new cache entry")
		return fmt.Errorf("error creating cache entry: %w", err)
	}
	return nil
}
