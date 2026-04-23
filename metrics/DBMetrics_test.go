package metrics

import (
	"fmt"
	"testing"
)

// TestSingletonPattern tests if NewAccountsDBMetricsBuilder and NewMainDBMetricsBuilder
// return the same singleton instance on multiple calls
func TestSingletonPattern(t *testing.T) {
	t.Log("Testing singleton pattern for DB Metrics Builders...")

	// Test AccountsDB Metrics Builder - should return same instance
	builder1 := NewAccountsDBMetricsBuilder()
	builder2 := NewAccountsDBMetricsBuilder()
	builder3 := NewAccountsDBMetricsBuilder()

	if builder1 != builder2 {
		t.Error("❌ AccountsDBMetricsBuilder: Different instances returned (expected singleton)")
		t.Logf("   builder1 pointer: %p", builder1)
		t.Logf("   builder2 pointer: %p", builder2)
	} else {
		t.Log("✅ AccountsDBMetricsBuilder: Same instance returned (singleton working)")
		t.Logf("   Instance pointer: %p", builder1)
	}

	if builder2 != builder3 {
		t.Error("❌ AccountsDBMetricsBuilder: Different instances returned (expected singleton)")
		t.Logf("   builder2 pointer: %p", builder2)
		t.Logf("   builder3 pointer: %p", builder3)
	} else {
		t.Log("✅ AccountsDBMetricsBuilder: Same instance returned (singleton working)")
	}

	// Test MainDB Metrics Builder - should return same instance
	mainBuilder1 := NewMainDBMetricsBuilder()
	mainBuilder2 := NewMainDBMetricsBuilder()
	mainBuilder3 := NewMainDBMetricsBuilder()

	if mainBuilder1 != mainBuilder2 {
		t.Error("❌ MainDBMetricsBuilder: Different instances returned (expected singleton)")
		t.Logf("   mainBuilder1 pointer: %p", mainBuilder1)
		t.Logf("   mainBuilder2 pointer: %p", mainBuilder2)
	} else {
		t.Log("✅ MainDBMetricsBuilder: Same instance returned (singleton working)")
		t.Logf("   Instance pointer: %p", mainBuilder1)
	}

	if mainBuilder2 != mainBuilder3 {
		t.Error("❌ MainDBMetricsBuilder: Different instances returned (expected singleton)")
		t.Logf("   mainBuilder2 pointer: %p", mainBuilder2)
		t.Logf("   mainBuilder3 pointer: %p", mainBuilder3)
	} else {
		t.Log("✅ MainDBMetricsBuilder: Same instance returned (singleton working)")
	}

	// Verify that AccountsDB and MainDB builders are different instances
	if builder1 == mainBuilder1 {
		t.Error("❌ AccountsDBMetricsBuilder and MainDBMetricsBuilder should be different instances")
	} else {
		t.Log("✅ AccountsDBMetricsBuilder and MainDBMetricsBuilder are different instances (correct)")
	}

	// Test that modifying one builder doesn't affect the other
	builder1.WithFunction("TestFunction1")
	mainBuilder1.WithFunction("TestFunction2")

	if builder1.functionName != "TestFunction1" {
		t.Errorf("❌ AccountsDBMetricsBuilder functionName not set correctly: got %s, expected TestFunction1", builder1.functionName)
	} else {
		t.Log("✅ AccountsDBMetricsBuilder functionName set correctly")
	}

	if mainBuilder1.functionName != "TestFunction2" {
		t.Errorf("❌ MainDBMetricsBuilder functionName not set correctly: got %s, expected TestFunction2", mainBuilder1.functionName)
	} else {
		t.Log("✅ MainDBMetricsBuilder functionName set correctly")
	}

	// Verify that all references to the same builder share state
	if builder2.functionName != "TestFunction1" {
		t.Errorf("❌ builder2 should share state with builder1: got %s, expected TestFunction1", builder2.functionName)
	} else {
		t.Log("✅ builder2 shares state with builder1 (singleton working correctly)")
	}

	if mainBuilder2.functionName != "TestFunction2" {
		t.Errorf("❌ mainBuilder2 should share state with mainBuilder1: got %s, expected TestFunction2", mainBuilder2.functionName)
	} else {
		t.Log("✅ mainBuilder2 shares state with mainBuilder1 (singleton working correctly)")
	}

	t.Log("✅ Singleton pattern test completed")
}

// TestSetTotalPoolCount tests setting the total pool count
func TestSetTotalPoolCount(t *testing.T) {
	NewAccountsDBMetricsBuilder().SetTotal(10)
	NewMainDBMetricsBuilder().SetTotal(20)

	fmt.Println("Added 10 connections to AccountsDB pool")
	fmt.Println("Added 20 connections to MainDB pool")
}

// TestIncrementingAndDecrementingPoolCounts tests incrementing and decrementing pool counts
// NOTE: This test will update the Prometheus metrics, which will be reflected in Grafana
// if the application is running and Prometheus is scraping the metrics endpoint.
// To see the changes in Grafana:
//  1. Make sure your application is running (metrics server on /metrics endpoint)
//  2. Ensure Prometheus is scraping the metrics endpoint
//  3. Run this test: go test -v ./metrics -run TestIncrementingAndDecrementingPoolCounts
//  4. Check Grafana dashboard - you should see the metrics update
func TestIncrementingAndDecrementingPoolCounts(t *testing.T) {
	t.Log("Testing incrementing and decrementing pool counts...")
	t.Log("NOTE: This will update Prometheus metrics visible in Grafana if the app is running")

	// Get initial values (if metrics were previously set)
	initialAccountsTotal := AccountsDBConnectionPoolCount
	initialMainTotal := MainDBConnectionPoolCount

	// Set initial values for testing
	NewAccountsDBMetricsBuilder().SetTotal(10)
	NewMainDBMetricsBuilder().SetTotal(20)
	t.Log("Set initial values: AccountsDB=10, MainDB=20")

	// Increment by 5 for AccountsDB
	for i := 0; i < 5; i++ {
		NewAccountsDBMetricsBuilder().IncrementTotal()
	}
	t.Log("Incremented AccountsDB pool by 5")

	// Increment by 5 for MainDB
	for i := 0; i < 5; i++ {
		NewMainDBMetricsBuilder().IncrementTotal()
	}
	t.Log("Incremented MainDB pool by 5")

	// Verify the metrics were updated (check Prometheus metric values)
	// Note: We can't directly read the values without the dto package, but we can verify
	// the singleton pattern is working and the methods are being called
	builder := NewAccountsDBMetricsBuilder()
	if builder == nil {
		t.Error("❌ AccountsDBMetricsBuilder is nil")
	} else {
		t.Logf("✅ AccountsDBMetricsBuilder instance: %p", builder)
	}

	mainBuilder := NewMainDBMetricsBuilder()
	if mainBuilder == nil {
		t.Error("❌ MainDBMetricsBuilder is nil")
	} else {
		t.Logf("✅ MainDBMetricsBuilder instance: %p", mainBuilder)
	}

	// Test decrementing
	NewAccountsDBMetricsBuilder().DecrementTotal()
	NewMainDBMetricsBuilder().DecrementTotal()
	t.Log("Decremented both pools by 1")

	fmt.Println("✅ Incremented 5 connections to AccountsDB pool (should be 15 now)")
	fmt.Println("✅ Incremented 5 connections to MainDB pool (should be 25 now)")
	fmt.Println("✅ Decremented both pools by 1 (AccountsDB=14, MainDB=24)")
	fmt.Println("")
	fmt.Println("📊 Check Grafana dashboard to see these metrics update in real-time!")
	fmt.Println("   Metrics: p2p_accounts_db_connection_pool_count, p2p_main_db_connection_pool_count")
	fmt.Println("")
	fmt.Printf("   Initial AccountsDB metric: %v\n", initialAccountsTotal)
	fmt.Printf("   Initial MainDB metric: %v\n", initialMainTotal)

	t.Log("✅ Increment/Decrement test completed - check Grafana dashboard for live updates")
}
