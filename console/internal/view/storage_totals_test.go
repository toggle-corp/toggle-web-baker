package view

import "testing"

func TestStorageCacheBytes(t *testing.T) {
	a := App{Storage: Storage{Volumes: []StorageVolume{
		{Name: "cache", Bytes: 100},
	}}}
	if got := a.StorageCacheBytes(); got != 100 {
		t.Errorf("StorageCacheBytes() = %d, want 100", got)
	}
	if got := (App{}).StorageCacheBytes(); got != 0 {
		t.Errorf("StorageCacheBytes() absent = %d, want 0", got)
	}
}

func TestStorageDataCacheBytes(t *testing.T) {
	a := App{Storage: Storage{Volumes: []StorageVolume{
		{Name: "dataCache", Bytes: 200},
	}}}
	if got := a.StorageDataCacheBytes(); got != 200 {
		t.Errorf("StorageDataCacheBytes() = %d, want 200", got)
	}
	if got := (App{}).StorageDataCacheBytes(); got != 0 {
		t.Errorf("StorageDataCacheBytes() absent = %d, want 0", got)
	}
}

func TestStorageOutputTotalBytes(t *testing.T) {
	a := App{Storage: Storage{Volumes: []StorageVolume{
		{Name: "outputTotal", Bytes: 300},
	}}}
	if got := a.StorageOutputTotalBytes(); got != 300 {
		t.Errorf("StorageOutputTotalBytes() = %d, want 300", got)
	}
	if got := (App{}).StorageOutputTotalBytes(); got != 0 {
		t.Errorf("StorageOutputTotalBytes() absent = %d, want 0", got)
	}
}

func TestStorageOutputActiveBytes(t *testing.T) {
	a := App{Storage: Storage{Volumes: []StorageVolume{
		{Name: "output", Bytes: 50},
	}}}
	if got := a.StorageOutputActiveBytes(); got != 50 {
		t.Errorf("StorageOutputActiveBytes() = %d, want 50", got)
	}
	if got := (App{}).StorageOutputActiveBytes(); got != 0 {
		t.Errorf("StorageOutputActiveBytes() absent = %d, want 0", got)
	}
}

func TestStorageTotalBytesExcludesActive(t *testing.T) {
	a := App{Storage: Storage{Volumes: []StorageVolume{
		{Name: "cache", Bytes: 100},
		{Name: "dataCache", Bytes: 200},
		{Name: "outputTotal", Bytes: 300},
		{Name: "output", Bytes: 50}, // active: subset of outputTotal, must NOT be counted
	}}}
	if got := a.StorageTotalBytes(); got != 600 {
		t.Errorf("StorageTotalBytes() = %d, want 600 (cache+dataCache+outputTotal, active excluded)", got)
	}
}

func TestAggregateStorageSumsApps(t *testing.T) {
	apps := []App{
		{Storage: Storage{Volumes: []StorageVolume{
			{Name: "cache", Bytes: 100},
			{Name: "dataCache", Bytes: 200},
			{Name: "outputTotal", Bytes: 300},
			{Name: "output", Bytes: 50},
		}}},
		{Storage: Storage{Volumes: []StorageVolume{
			{Name: "cache", Bytes: 1},
			{Name: "dataCache", Bytes: 2},
			{Name: "outputTotal", Bytes: 3},
			{Name: "output", Bytes: 1},
		}}},
	}
	want := StorageTotals{Cache: 101, DataCache: 202, OutputTotal: 303, OutputActive: 51, Grand: 606}
	if got := AggregateStorage(apps); got != want {
		t.Errorf("AggregateStorage() = %+v, want %+v", got, want)
	}
}

func TestAggregateStorageGrandExcludesActive(t *testing.T) {
	// A large active output must not inflate Grand: it is a subset of outputTotal.
	apps := []App{{Storage: Storage{Volumes: []StorageVolume{
		{Name: "cache", Bytes: 10},
		{Name: "outputTotal", Bytes: 40},
		{Name: "output", Bytes: 40},
	}}}}
	got := AggregateStorage(apps)
	if got.Grand != 50 {
		t.Errorf("Grand = %d, want 50 (cache+outputTotal, active excluded)", got.Grand)
	}
	if got.OutputActive != 40 {
		t.Errorf("OutputActive = %d, want 40 (still reported)", got.OutputActive)
	}
}

func TestStorageTooltip(t *testing.T) {
	a := App{Storage: Storage{Volumes: []StorageVolume{
		{Name: "cache", Bytes: 4 * 1024 * 1024 * 1024},
		{Name: "dataCache", Bytes: 2 * 1024 * 1024 * 1024},
		{Name: "outputTotal", Bytes: 6871947674},
		{Name: "output", Bytes: 3 * 1024 * 1024 * 1024},
	}}}
	want := "Cache 4.0 GiB · Data cache 2.0 GiB · Output 6.4 GiB (active 3.0 GiB)"
	if got := a.StorageTooltip(); got != want {
		t.Errorf("StorageTooltip() = %q, want %q", got, want)
	}
}

func TestStorageTotalsSinglePass(t *testing.T) {
	a := App{Storage: Storage{Volumes: []StorageVolume{
		{Name: "cache", Bytes: 100},
		{Name: "dataCache", Bytes: 200},
		{Name: "outputTotal", Bytes: 300},
		{Name: "output", Bytes: 50}, // active: subset of outputTotal, excluded from Grand
		{Name: "unknown", Bytes: 999},
	}}}
	got := a.storageTotals()
	want := StorageTotals{Cache: 100, DataCache: 200, OutputTotal: 300, OutputActive: 50, Grand: 600}
	if got != want {
		t.Errorf("storageTotals() = %+v, want %+v", got, want)
	}
	// Empty app is all zeros.
	if got := (App{}).storageTotals(); got != (StorageTotals{}) {
		t.Errorf("storageTotals() empty = %+v, want zero", got)
	}
}

func TestStorageTotalsHumanAccessors(t *testing.T) {
	t2 := StorageTotals{
		Cache:        4 * 1024 * 1024 * 1024,
		DataCache:    2 * 1024 * 1024 * 1024,
		OutputTotal:  6871947674,
		OutputActive: 3 * 1024 * 1024 * 1024,
		Grand:        4*1024*1024*1024 + 2*1024*1024*1024 + 6871947674,
	}
	if got := t2.CacheHuman(); got != "4.0 GiB" {
		t.Errorf("CacheHuman() = %q, want 4.0 GiB", got)
	}
	if got := t2.DataCacheHuman(); got != "2.0 GiB" {
		t.Errorf("DataCacheHuman() = %q, want 2.0 GiB", got)
	}
	if got := t2.OutputHuman(); got != "6.4 GiB" {
		t.Errorf("OutputHuman() = %q, want 6.4 GiB", got)
	}
	if got := t2.ActiveHuman(); got != "3.0 GiB" {
		t.Errorf("ActiveHuman() = %q, want 3.0 GiB", got)
	}
	if got := t2.GrandHuman(); got != "12.4 GiB" {
		t.Errorf("GrandHuman() = %q, want 12.4 GiB", got)
	}
}

func TestStorageTotalHuman(t *testing.T) {
	a := App{Storage: Storage{Volumes: []StorageVolume{
		{Name: "cache", Bytes: 100}, {Name: "outputTotal", Bytes: 924},
	}}}
	if got := a.StorageTotalHuman(); got != "1.0 KiB" {
		t.Errorf("StorageTotalHuman() = %q, want %q", got, "1.0 KiB")
	}
}
