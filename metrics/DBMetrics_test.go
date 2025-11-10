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
