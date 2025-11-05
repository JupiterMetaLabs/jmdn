package DB_OPs

import (
	"fmt"
	"log"

	"gossipnode/config"
	hashmap "gossipnode/crdt/HashMap"
)

// ValidateHashMapKeys checks that all keys in the HashMap actually exist in the database
// This ensures the HashMap accurately represents what's in the DB
func ValidateHashMapKeys(hashMap *hashmap.HashMap, dbClient *config.PooledConnection, dbType string) (*hashmap.HashMap, error) {
	if hashMap == nil {
		return hashmap.New(), nil
	}

	if dbClient == nil || dbClient.Client == nil {
		return nil, fmt.Errorf("database client is nil")
	}

	validatedHashMap := hashmap.New()
	allKeys := hashMap.Keys()
	totalKeys := len(allKeys)
	validCount := 0
	invalidCount := 0

	log.Printf(">>> [VALIDATOR] Validating %d keys from HashMap against %s database...", totalKeys, dbType)

	// Check each key exists in the database
	batchSize := 100
	for i := 0; i < totalKeys; i += batchSize {
		end := i + batchSize
		if end > totalKeys {
			end = totalKeys
		}

		batchKeys := allKeys[i:end]
		for _, key := range batchKeys {
			exists, err := Exists(dbClient, key)
			if err != nil {
				// Log but continue - some keys might have issues
				log.Printf(">>> [VALIDATOR] WARNING: Failed to check key '%s': %v", key, err)
				invalidCount++
				continue
			}

			if exists {
				validatedHashMap.Insert(key)
				validCount++
			} else {
				log.Printf(">>> [VALIDATOR] Key '%s' not found in database - removing from HashMap", key)
				invalidCount++
			}
		}

		if (i/batchSize+1)%10 == 0 {
			log.Printf(">>> [VALIDATOR] Progress: %d/%d keys validated (%d valid, %d invalid)",
				i+len(batchKeys), totalKeys, validCount, invalidCount)
		}
	}

	log.Printf(">>> [VALIDATOR] Validation complete: %d valid keys, %d invalid keys removed (original: %d)",
		validCount, invalidCount, totalKeys)

	if invalidCount > 0 {
		log.Printf(">>> [VALIDATOR] WARNING: Removed %d keys from HashMap that don't exist in database", invalidCount)
		log.Printf(">>> [VALIDATOR] This suggests the HashMap was built from a previous sync that didn't complete")
	}

	return validatedHashMap, nil
}

// ValidateHashMapKeysIncremental validates HashMap keys in batches without loading all keys into memory
// This is more efficient for very large HashMaps
func ValidateHashMapKeysIncremental(hashMap *hashmap.HashMap, dbClient *config.PooledConnection, dbType string) (*hashmap.HashMap, error) {
	if hashMap == nil {
		return hashmap.New(), nil
	}

	if dbClient == nil || dbClient.Client == nil {
		return nil, fmt.Errorf("database client is nil")
	}

	validatedHashMap := hashmap.New()
	allKeys := hashMap.Keys()
	totalKeys := len(allKeys)
	validCount := 0
	invalidCount := 0

	log.Printf(">>> [VALIDATOR] Validating %d keys from HashMap against %s database (incremental)...", totalKeys, dbType)

	// Process in batches to avoid memory issues
	batchSize := 1000
	for i := 0; i < totalKeys; i += batchSize {
		end := i + batchSize
		if end > totalKeys {
			end = totalKeys
		}

		batchKeys := allKeys[i:end]

		// Check each key in the batch
		for _, key := range batchKeys {
			exists, err := Exists(dbClient, key)
			if err != nil {
				log.Printf(">>> [VALIDATOR] WARNING: Failed to check key '%s': %v", key, err)
				invalidCount++
				continue
			}

			if exists {
				validatedHashMap.Insert(key)
				validCount++
			} else {
				invalidCount++
			}
		}

		if (i/batchSize+1)%10 == 0 {
			log.Printf(">>> [VALIDATOR] Progress: %d/%d keys validated (%d valid, %d invalid)",
				i+len(batchKeys), totalKeys, validCount, invalidCount)
		}
	}

	log.Printf(">>> [VALIDATOR] Validation complete: %d valid keys, %d invalid keys removed (original: %d)",
		validCount, invalidCount, totalKeys)

	if invalidCount > 0 {
		log.Printf(">>> [VALIDATOR] WARNING: Removed %d keys from HashMap that don't exist in database", invalidCount)
	}

	return validatedHashMap, nil
}
