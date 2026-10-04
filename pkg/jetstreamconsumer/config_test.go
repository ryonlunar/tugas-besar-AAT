package jetstreamconsumer

import "testing"

func TestMaxDeliverConfiguration(t *testing.T) {
	t.Setenv("NATS_TOKEN", "test-token")
	for _, test := range []struct {
		value string
		want  int
		valid bool
	}{
		{"", -1, true},
		{"-1", -1, true},
		{"7", 7, true},
		{"0", 0, false},
		{"-2", 0, false},
		{"invalid", 0, false},
	} {
		t.Run(test.value, func(t *testing.T) {
			t.Setenv("NATS_MAX_DELIVER", test.value)
			config, err := ConfigFromEnv("NATS_DASHBOARD_DURABLE", "dashboard-v1")
			if test.valid {
				if err != nil || config.MaxDeliver != test.want {
					t.Fatalf("MaxDeliver = %d, err = %v; want %d", config.MaxDeliver, err, test.want)
				}
			} else if err == nil {
				t.Fatalf("accepted invalid NATS_MAX_DELIVER %q", test.value)
			}
		})
	}
}
