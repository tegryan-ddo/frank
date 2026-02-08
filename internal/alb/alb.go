package alb

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
)

const (
	// StackName is the CloudFormation stack name for Frank
	StackName = "FrankStack"

	// TargetGroupPrefix for profile target groups
	TargetGroupPrefix = "frank-profile-"

	// ProfileTagKey is the tag key for identifying profile resources
	ProfileTagKey = "frank-profile"

	// Health check settings
	HealthCheckPath     = "/health"
	HealthCheckPort     = "7683"
	HealthCheckInterval = 30
	HealthCheckTimeout  = 10

	// Port definitions
	WebPort    = 7680 // Web server (HTML wrapper)
	ClaudePort = 7681 // Claude ttyd terminal
	BashPort   = 7682 // Bash ttyd terminal
	StatusPort = 7683 // Status server

	// TargetPort is the port for ALB target groups (use web port for HTML wrapper)
	TargetPort = WebPort
)

// Infrastructure holds discovered AWS infrastructure details
type Infrastructure struct {
	VPCID           string
	ALBArn          string
	ListenerArn     string
	SubnetIDs       []string
	SecurityGroupID string
}

// Manager handles ALB operations for profile routing
type Manager struct {
	elbClient *elasticloadbalancingv2.Client
	cfnClient *cloudformation.Client
	infra     *Infrastructure
}

// NewManager creates a new ALB manager
func NewManager(ctx context.Context) (*Manager, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	m := &Manager{
		elbClient: elasticloadbalancingv2.NewFromConfig(cfg),
		cfnClient: cloudformation.NewFromConfig(cfg),
	}

	return m, nil
}

// DiscoverInfrastructure finds ALB and VPC details from CloudFormation stack
func (m *Manager) DiscoverInfrastructure(ctx context.Context) (*Infrastructure, error) {
	if m.infra != nil {
		return m.infra, nil
	}

	// Get stack outputs
	output, err := m.cfnClient.DescribeStacks(ctx, &cloudformation.DescribeStacksInput{
		StackName: aws.String(StackName),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to describe stack %s: %w", StackName, err)
	}

	if len(output.Stacks) == 0 {
		return nil, fmt.Errorf("stack %s not found", StackName)
	}

	stack := output.Stacks[0]
	infra := &Infrastructure{}

	// Extract outputs
	for _, o := range stack.Outputs {
		key := aws.ToString(o.OutputKey)
		value := aws.ToString(o.OutputValue)

		switch key {
		case "AlbDnsName":
			// We need to find the ALB ARN from the DNS name
			// This requires listing ALBs and matching
		}
		_ = value // Will use outputs once we add them to CDK
	}

	// Since CDK doesn't export all we need, let's find resources by tags
	// List all ALBs and find the one named "frank-alb"
	albOutput, err := m.elbClient.DescribeLoadBalancers(ctx, &elasticloadbalancingv2.DescribeLoadBalancersInput{
		Names: []string{"frank-alb"},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to find ALB: %w", err)
	}

	if len(albOutput.LoadBalancers) == 0 {
		return nil, fmt.Errorf("ALB 'frank-alb' not found")
	}

	alb := albOutput.LoadBalancers[0]
	infra.ALBArn = aws.ToString(alb.LoadBalancerArn)
	infra.VPCID = aws.ToString(alb.VpcId)
	infra.SecurityGroupID = alb.SecurityGroups[0]

	// Get the HTTPS listener
	listeners, err := m.elbClient.DescribeListeners(ctx, &elasticloadbalancingv2.DescribeListenersInput{
		LoadBalancerArn: alb.LoadBalancerArn,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to describe listeners: %w", err)
	}

	for _, l := range listeners.Listeners {
		if l.Port != nil && *l.Port == 443 {
			infra.ListenerArn = aws.ToString(l.ListenerArn)
			break
		}
	}

	if infra.ListenerArn == "" {
		return nil, fmt.Errorf("HTTPS listener not found on ALB")
	}

	m.infra = infra
	return infra, nil
}

// EnsureTargetGroup creates a target group for the profile if it doesn't exist
func (m *Manager) EnsureTargetGroup(ctx context.Context, profileName string) (string, error) {
	infra, err := m.DiscoverInfrastructure(ctx)
	if err != nil {
		return "", err
	}

	tgName := TargetGroupPrefix + profileName
	if len(tgName) > 32 {
		// Target group names are limited to 32 chars
		tgName = tgName[:32]
	}

	// Check if target group already exists
	existing, err := m.elbClient.DescribeTargetGroups(ctx, &elasticloadbalancingv2.DescribeTargetGroupsInput{
		Names: []string{tgName},
	})
	if err == nil && len(existing.TargetGroups) > 0 {
		return aws.ToString(existing.TargetGroups[0].TargetGroupArn), nil
	}

	// Create new target group
	createOutput, err := m.elbClient.CreateTargetGroup(ctx, &elasticloadbalancingv2.CreateTargetGroupInput{
		Name:       aws.String(tgName),
		Protocol:   elbv2types.ProtocolEnumHttp,
		Port:       aws.Int32(TargetPort),
		VpcId:      aws.String(infra.VPCID),
		TargetType: elbv2types.TargetTypeEnumIp,
		HealthCheckEnabled:         aws.Bool(true),
		HealthCheckPath:            aws.String(HealthCheckPath),
		HealthCheckPort:            aws.String(HealthCheckPort),
		HealthCheckProtocol:        elbv2types.ProtocolEnumHttp,
		HealthCheckIntervalSeconds: aws.Int32(HealthCheckInterval),
		HealthCheckTimeoutSeconds:  aws.Int32(HealthCheckTimeout),
		HealthyThresholdCount:      aws.Int32(2),
		UnhealthyThresholdCount:    aws.Int32(3),
		Matcher: &elbv2types.Matcher{
			HttpCode: aws.String("200"),
		},
		Tags: []elbv2types.Tag{
			{
				Key:   aws.String(ProfileTagKey),
				Value: aws.String(profileName),
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to create target group: %w", err)
	}

	if len(createOutput.TargetGroups) == 0 {
		return "", fmt.Errorf("no target group returned after creation")
	}

	return aws.ToString(createOutput.TargetGroups[0].TargetGroupArn), nil
}

// EnsureListenerRule creates a listener rule for the profile if it doesn't exist
// Uses path-based routing: /<profile>/* -> target group
func (m *Manager) EnsureListenerRule(ctx context.Context, profileName, targetGroupArn string) error {
	infra, err := m.DiscoverInfrastructure(ctx)
	if err != nil {
		return err
	}

	// Check if rule already exists by listing rules and checking conditions
	rules, err := m.elbClient.DescribeRules(ctx, &elasticloadbalancingv2.DescribeRulesInput{
		ListenerArn: aws.String(infra.ListenerArn),
	})
	if err != nil {
		return fmt.Errorf("failed to describe listener rules: %w", err)
	}

	pathPattern := fmt.Sprintf("/%s/*", profileName)

	for _, rule := range rules.Rules {
		for _, cond := range rule.Conditions {
			if cond.PathPatternConfig != nil {
				for _, val := range cond.PathPatternConfig.Values {
					if val == pathPattern {
						// Rule already exists
						return nil
					}
				}
			}
		}
	}

	// Calculate priority based on profile name hash (100-999 range)
	priority := hashToPriority(profileName)

	// Create listener rule with path-based routing
	_, err = m.elbClient.CreateRule(ctx, &elasticloadbalancingv2.CreateRuleInput{
		ListenerArn: aws.String(infra.ListenerArn),
		Priority:    aws.Int32(priority),
		Conditions: []elbv2types.RuleCondition{
			{
				Field: aws.String("path-pattern"),
				PathPatternConfig: &elbv2types.PathPatternConditionConfig{
					Values: []string{pathPattern},
				},
			},
		},
		Actions: []elbv2types.Action{
			{
				Type:           elbv2types.ActionTypeEnumForward,
				TargetGroupArn: aws.String(targetGroupArn),
			},
		},
		Tags: []elbv2types.Tag{
			{
				Key:   aws.String(ProfileTagKey),
				Value: aws.String(profileName),
			},
		},
	})
	if err != nil {
		// Check if it's a priority conflict
		if strings.Contains(err.Error(), "PriorityInUse") {
			// Try with a different priority
			for i := 1; i <= 10; i++ {
				priority = priority + int32(i)
				_, err = m.elbClient.CreateRule(ctx, &elasticloadbalancingv2.CreateRuleInput{
					ListenerArn: aws.String(infra.ListenerArn),
					Priority:    aws.Int32(priority),
					Conditions: []elbv2types.RuleCondition{
						{
							Field: aws.String("path-pattern"),
							PathPatternConfig: &elbv2types.PathPatternConditionConfig{
								Values: []string{pathPattern},
							},
						},
					},
					Actions: []elbv2types.Action{
						{
							Type:           elbv2types.ActionTypeEnumForward,
							TargetGroupArn: aws.String(targetGroupArn),
						},
					},
				})
				if err == nil {
					return nil
				}
			}
		}
		return fmt.Errorf("failed to create listener rule: %w", err)
	}

	return nil
}

// RegisterTarget registers a task IP in the target group
func (m *Manager) RegisterTarget(ctx context.Context, targetGroupArn, ip string, port int) error {
	_, err := m.elbClient.RegisterTargets(ctx, &elasticloadbalancingv2.RegisterTargetsInput{
		TargetGroupArn: aws.String(targetGroupArn),
		Targets: []elbv2types.TargetDescription{
			{
				Id:   aws.String(ip),
				Port: aws.Int32(int32(port)),
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to register target: %w", err)
	}
	return nil
}

// DeregisterTarget removes a task IP from the target group
func (m *Manager) DeregisterTarget(ctx context.Context, targetGroupArn, ip string, port int) error {
	_, err := m.elbClient.DeregisterTargets(ctx, &elasticloadbalancingv2.DeregisterTargetsInput{
		TargetGroupArn: aws.String(targetGroupArn),
		Targets: []elbv2types.TargetDescription{
			{
				Id:   aws.String(ip),
				Port: aws.Int32(int32(port)),
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to deregister target: %w", err)
	}
	return nil
}

// GetTargetGroupArn finds the target group ARN for a profile
func (m *Manager) GetTargetGroupArn(ctx context.Context, profileName string) (string, error) {
	tgName := TargetGroupPrefix + profileName
	if len(tgName) > 32 {
		tgName = tgName[:32]
	}

	existing, err := m.elbClient.DescribeTargetGroups(ctx, &elasticloadbalancingv2.DescribeTargetGroupsInput{
		Names: []string{tgName},
	})
	if err != nil {
		return "", fmt.Errorf("target group not found for profile %s", profileName)
	}

	if len(existing.TargetGroups) == 0 {
		return "", fmt.Errorf("target group not found for profile %s", profileName)
	}

	return aws.ToString(existing.TargetGroups[0].TargetGroupArn), nil
}

// DeleteTargetGroup removes a target group for a profile
func (m *Manager) DeleteTargetGroup(ctx context.Context, profileName string) error {
	tgArn, err := m.GetTargetGroupArn(ctx, profileName)
	if err != nil {
		return nil // Target group doesn't exist, nothing to delete
	}

	_, err = m.elbClient.DeleteTargetGroup(ctx, &elasticloadbalancingv2.DeleteTargetGroupInput{
		TargetGroupArn: aws.String(tgArn),
	})
	if err != nil {
		return fmt.Errorf("failed to delete target group: %w", err)
	}

	return nil
}

// DeleteListenerRule removes the listener rule for a profile
func (m *Manager) DeleteListenerRule(ctx context.Context, profileName string) error {
	infra, err := m.DiscoverInfrastructure(ctx)
	if err != nil {
		return err
	}

	// Find the rule by path pattern condition
	rules, err := m.elbClient.DescribeRules(ctx, &elasticloadbalancingv2.DescribeRulesInput{
		ListenerArn: aws.String(infra.ListenerArn),
	})
	if err != nil {
		return fmt.Errorf("failed to describe listener rules: %w", err)
	}

	pathPattern := fmt.Sprintf("/%s/*", profileName)

	for _, rule := range rules.Rules {
		for _, cond := range rule.Conditions {
			if cond.PathPatternConfig != nil {
				for _, val := range cond.PathPatternConfig.Values {
					if val == pathPattern {
						// Delete this rule
						_, err = m.elbClient.DeleteRule(ctx, &elasticloadbalancingv2.DeleteRuleInput{
							RuleArn: rule.RuleArn,
						})
						if err != nil {
							return fmt.Errorf("failed to delete listener rule: %w", err)
						}
						return nil
					}
				}
			}
		}
	}

	return nil // Rule not found, nothing to delete
}

// targetGroupSuffixes are the suffixes used for profile target groups
var targetGroupSuffixes = []string{"", "-t", "-b"}

// FindOrphanedTargetGroups lists all frank-profile-* target groups and returns
// profile names that are not in the runningProfiles set.
func (m *Manager) FindOrphanedTargetGroups(ctx context.Context, runningProfiles map[string]bool) ([]string, error) {
	var marker *string
	seenProfiles := make(map[string]bool)

	for {
		input := &elasticloadbalancingv2.DescribeTargetGroupsInput{
			Marker: marker,
		}
		result, err := m.elbClient.DescribeTargetGroups(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("failed to describe target groups: %w", err)
		}

		for _, tg := range result.TargetGroups {
			name := aws.ToString(tg.TargetGroupName)
			if !strings.HasPrefix(name, TargetGroupPrefix) {
				continue
			}
			// Strip prefix and known suffixes to get profile name
			profileName := strings.TrimPrefix(name, TargetGroupPrefix)
			for _, suffix := range targetGroupSuffixes[1:] { // skip empty suffix
				profileName = strings.TrimSuffix(profileName, suffix)
			}
			seenProfiles[profileName] = true
		}

		marker = result.NextMarker
		if marker == nil {
			break
		}
	}

	var orphans []string
	for profileName := range seenProfiles {
		if !runningProfiles[profileName] {
			orphans = append(orphans, profileName)
		}
	}

	return orphans, nil
}

// DeleteAllTargetGroups removes all target groups (main, -t, -b) for a profile
func (m *Manager) DeleteAllTargetGroups(ctx context.Context, profileName string) error {
	for _, suffix := range targetGroupSuffixes {
		tgName := TargetGroupPrefix + profileName + suffix
		if len(tgName) > 32 {
			tgName = tgName[:32]
		}

		existing, err := m.elbClient.DescribeTargetGroups(ctx, &elasticloadbalancingv2.DescribeTargetGroupsInput{
			Names: []string{tgName},
		})
		if err != nil {
			// Target group not found, skip
			continue
		}

		if len(existing.TargetGroups) > 0 {
			tgArn := aws.ToString(existing.TargetGroups[0].TargetGroupArn)
			_, err = m.elbClient.DeleteTargetGroup(ctx, &elasticloadbalancingv2.DeleteTargetGroupInput{
				TargetGroupArn: aws.String(tgArn),
			})
			if err != nil {
				return fmt.Errorf("failed to delete target group %s: %w", tgName, err)
			}
		}
	}
	return nil
}

// DeleteAllListenerRules removes all listener rules for a profile (main, _t, _b, status)
func (m *Manager) DeleteAllListenerRules(ctx context.Context, profileName string) error {
	infra, err := m.DiscoverInfrastructure(ctx)
	if err != nil {
		return err
	}

	rules, err := m.elbClient.DescribeRules(ctx, &elasticloadbalancingv2.DescribeRulesInput{
		ListenerArn: aws.String(infra.ListenerArn),
	})
	if err != nil {
		return fmt.Errorf("failed to describe listener rules: %w", err)
	}

	// Path patterns that belong to this profile
	profilePaths := []string{
		fmt.Sprintf("/%s/*", profileName),
		fmt.Sprintf("/%s", profileName),
		fmt.Sprintf("/%s/_t", profileName),
		fmt.Sprintf("/%s/_t/*", profileName),
		fmt.Sprintf("/%s/_b", profileName),
		fmt.Sprintf("/%s/_b/*", profileName),
		fmt.Sprintf("/%s/status", profileName),
		fmt.Sprintf("/%s/status/*", profileName),
	}
	pathSet := make(map[string]bool)
	for _, p := range profilePaths {
		pathSet[p] = true
	}

	for _, rule := range rules.Rules {
		if rule.IsDefault != nil && *rule.IsDefault {
			continue
		}
		for _, cond := range rule.Conditions {
			if cond.PathPatternConfig != nil {
				for _, val := range cond.PathPatternConfig.Values {
					if pathSet[val] {
						_, err = m.elbClient.DeleteRule(ctx, &elasticloadbalancingv2.DeleteRuleInput{
							RuleArn: rule.RuleArn,
						})
						if err != nil {
							return fmt.Errorf("failed to delete listener rule: %w", err)
						}
						goto nextRule // Rule deleted, move to next
					}
				}
			}
		}
	nextRule:
	}

	return nil
}

// hashToPriority converts a profile name to a listener rule priority (100-999)
func hashToPriority(name string) int32 {
	h := fnv.New32a()
	h.Write([]byte(name))
	// Map to 100-999 range (leaving 1-99 for static rules)
	return int32(100 + (h.Sum32() % 900))
}
