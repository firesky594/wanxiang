package planning

type Plan struct {
	Summary             string     `json:"summary"`
	RequiresProjectLead bool       `json:"requires_project_lead"`
	WorkItems           []WorkItem `json:"work_items"`
}

type WorkItem struct {
	Key                  string   `json:"key"`
	Title                string   `json:"title"`
	Description          string   `json:"description"`
	Kind                 string   `json:"kind"`
	RequiredCapabilities []string `json:"required_capabilities"`
	RequiredSkills       []string `json:"required_skills"`
	RequiredMCPs         []string `json:"required_mcps"`
	AcceptanceCriteria   []string `json:"acceptance_criteria"`
	DependsOn            []string `json:"depends_on"`
}
