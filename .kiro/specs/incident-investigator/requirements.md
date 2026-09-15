# Requirements Document

## Introduction

The Incident Investigator Agent is an AI-powered tool that accepts a user-provided incident prompt and autonomously investigates the incident by reasoning over multiple data sources. It produces a structured investigation summary containing a narrative summary, supporting evidence, likely root cause, confidence level, and recommended remediation actions. The agent orchestrates four tools — incident retrieval, deployment history, knowledge base search, and service health checks — to gather context before synthesizing its findings.

## Glossary

- **Agent**: The Incident Investigator Agent that processes user prompts and coordinates tool calls to investigate incidents.
- **Investigation_Summary**: The structured output produced by the Agent, containing: Summary, Evidence, Likely_Root_Cause, Confidence_Level, and Recommended_Actions.
- **Incident_Prompt**: The natural-language input provided by the user describing or referencing the incident to investigate.
- **Tool**: An external function the Agent may call to retrieve data (get_incident, get_recent_deployments, search_knowledge_base, get_service_health).
- **Evidence**: A list of discrete, sourced data points gathered from Tool calls that inform the investigation.
- **Confidence_Level**: A categorical rating (Low, Medium, High) expressing the Agent's certainty in the Likely_Root_Cause.
- **Root_Cause**: The primary technical reason identified as the origin of the incident.
- **Knowledge_Base**: A searchable repository of past incidents, runbooks, and operational documentation.

---

## Requirements

### Requirement 1: Accept and Validate Incident Prompts

**User Story:** As an on-call engineer, I want to provide a natural-language incident description so that the Agent can begin an investigation without requiring structured input.

#### Acceptance Criteria

1. THE Agent SHALL accept an Incident_Prompt as a non-empty string input of 1 to 10,000 characters.
2. IF the Incident_Prompt is empty or contains only whitespace, THEN THE Agent SHALL return an error message indicating that a valid prompt is required, without beginning the investigation workflow.
3. IF the Incident_Prompt exceeds 10,000 characters, THEN THE Agent SHALL return an error message indicating the input exceeds the maximum allowed length, without beginning the investigation workflow.
4. WHEN a valid Incident_Prompt is received, THE Agent SHALL begin the investigation workflow within 2 seconds of receipt.

---

### Requirement 2: Retrieve Incident Details

**User Story:** As an on-call engineer, I want the Agent to fetch the relevant incident record so that the investigation is grounded in authoritative incident data.

#### Acceptance Criteria

1. WHEN an investigation is initiated, THE Agent SHALL call `get_incident()` with the incident identifier extracted from the Incident_Prompt to retrieve the associated incident record.
2. IF `get_incident()` returns an error or empty response, THEN THE Agent SHALL record the failure as an Evidence item that includes the tool name and the error indication returned, and continue the investigation with the remaining tools.
3. WHEN `get_incident()` returns a valid record, THE Agent SHALL include the incident identifier, severity, and list of affected services in the Evidence list before proceeding to subsequent investigation steps.
4. IF no incident identifier can be extracted from the Incident_Prompt, THEN THE Agent SHALL record this as an Evidence item noting the identifier was not found, and continue the investigation using the full Incident_Prompt text as context for the remaining tools.

---

### Requirement 3: Retrieve Recent Deployment History

**User Story:** As an on-call engineer, I want the Agent to check recent deployments so that deployment-induced regressions can be identified as a potential root cause.

#### Acceptance Criteria

1. WHEN an investigation is initiated, THE Agent SHALL call `get_recent_deployments()` with a time window spanning from 60 minutes before the incident timestamp to the incident timestamp.
2. IF `get_recent_deployments()` returns an error or empty response, THEN THE Agent SHALL record the failure as an Evidence item with a status of "unavailable" and continue the investigation with the remaining tools.
3. WHEN `get_recent_deployments()` returns one or more deployment records, THE Agent SHALL include each deployment's service name, version, and timestamp in the Evidence list.
4. IF a deployment record is missing one or more of the required fields (service name, version, or timestamp), THEN THE Agent SHALL record the incomplete record in the Evidence list with the available fields and mark the missing fields as "unknown".
5. WHEN `get_recent_deployments()` returns more than 50 deployment records, THE Agent SHALL include only the 50 records closest to the incident timestamp in the Evidence list.

---

### Requirement 4: Search the Knowledge Base

**User Story:** As an on-call engineer, I want the Agent to query past incidents and runbooks so that known solutions and historical patterns inform the investigation.

#### Acceptance Criteria

1. WHEN an investigation is initiated, THE Agent SHALL call `search_knowledge_base()` with a query derived from the Incident_Prompt and retrieved incident details, containing at least the incident title and a description of at most 500 characters.
2. IF `search_knowledge_base()` returns an error or empty response, THEN THE Agent SHALL record the failure as an Evidence item with a note indicating the knowledge base was unavailable or returned no results, and continue the investigation with the remaining tools.
3. WHEN `search_knowledge_base()` returns results, THE Agent SHALL include in the Evidence list each result whose relevance score meets the minimum threshold, recording the result's title and a relevance summary of at most 200 characters.
4. IF `search_knowledge_base()` does not return a response within 10 seconds, THEN THE Agent SHALL treat the call as a failed attempt, record the timeout as an Evidence item, and continue the investigation with the remaining tools.
5. THE Agent SHALL call `search_knowledge_base()` at most once per investigation initiation and SHALL NOT retry on error or timeout.

---

### Requirement 5: Check Service Health

**User Story:** As an on-call engineer, I want the Agent to check live service health so that currently degraded or failing services are reflected in the investigation.

#### Acceptance Criteria

1. WHEN an investigation is initiated, THE Agent SHALL call `get_service_health()` for each service identified in the incident record.
2. IF `get_service_health()` returns an error, times out, or returns a response with no health status field, THEN THE Agent SHALL record the failure as an Evidence item containing the service name and the reason health data could not be retrieved, and continue the investigation with the remaining services and tools.
3. WHEN `get_service_health()` returns health data, THE Agent SHALL include each service's name and health status value as reported by the tool (e.g., healthy, degraded, or down) in the Evidence list.
4. IF the incident record contains no identifiable services, THEN THE Agent SHALL record that no services were found for health checking as an Evidence item and proceed with the investigation.

---

### Requirement 6: Reason Over Collected Evidence

**User Story:** As an on-call engineer, I want the Agent to synthesize all gathered data into coherent findings so that I receive a unified, reasoned analysis rather than raw data.

#### Acceptance Criteria

1. WHEN all Tool calls have completed or returned a failure response, THE Agent SHALL reason over the complete Evidence list to identify the most probable Root_Cause and populate the Likely_Root_Cause field.
2. THE Agent SHALL assign Confidence_Level according to the following rules: "High" if two or more Evidence items independently support the same Root_Cause without contradiction; "Medium" if at least one successful Evidence item supports the Root_Cause but other Evidence items are absent or inconclusive; "Low" if no successful Tool responses exist or all Evidence items are inconclusive.
3. WHILE Evidence contains no successful Tool responses, THE Agent SHALL set Confidence_Level to "Low" and include a statement in the Investigation_Summary indicating that no Tool responses were available to support the analysis.
4. THE Agent SHALL populate Likely_Root_Cause with a value that explicitly names the source field (tool name and result identifier) of at least one Evidence item that supports the stated Root_Cause.
5. IF Evidence contains two or more successful Tool responses that attribute the Root_Cause to mutually exclusive conditions, THEN THE Agent SHALL set Confidence_Level to "Low" and include a statement in the Investigation_Summary identifying the contradicting Evidence sources by tool name.

---

### Requirement 7: Produce Structured Investigation Summary

**User Story:** As an on-call engineer, I want a consistently structured investigation report so that I can quickly understand the situation and take action without parsing unstructured text.

#### Acceptance Criteria

1. WHEN reasoning is complete, THE Agent SHALL produce an Investigation_Summary containing all five fields: Summary, Evidence, Likely_Root_Cause, Confidence_Level, and Recommended_Actions.
2. THE Agent SHALL populate the Summary field with a plain-language narrative of no more than 200 words describing the incident context, affected components, and key findings.
3. THE Agent SHALL populate the Evidence field as an ordered list where each item includes the source tool name and a plain-language description of the data point; if no evidence was gathered, THE Agent SHALL include a single item stating that no data was retrieved.
4. THE Agent SHALL populate the Likely_Root_Cause field with a single statement of no more than 100 words identifying the most probable cause; IF no root cause can be determined from the Evidence, THEN THE Agent SHALL populate the field with an explicit statement indicating the cause is undetermined.
5. THE Agent SHALL populate the Confidence_Level field with one of three discrete values: Low, Medium, or High, where Low indicates fewer than two corroborating evidence items, Medium indicates two or more corroborating items without a confirming signal, and High indicates two or more corroborating items with at least one confirming signal.
6. THE Agent SHALL populate the Recommended_Actions field as an ordered list of one or more concrete, actionable steps sorted from highest to lowest priority, where each step identifies a specific action and its target component or owner.
7. IF no Recommended_Actions can be derived from the Evidence, THEN THE Agent SHALL populate the Recommended_Actions field with a single default step directing manual escalation to the service owner identified in the incident context, or to the on-call team if no service owner is identified.

---

### Requirement 8: Handle Partial Tool Failures Gracefully

**User Story:** As an on-call engineer, I want the Agent to produce a summary even when some tools fail so that a partial investigation is available rather than a complete failure.

#### Acceptance Criteria

1. IF one or more Tool calls fail, THEN THE Agent SHALL continue the investigation using the results from the remaining successful Tool calls and SHALL NOT retry failed Tool calls.
2. WHEN the Investigation_Summary is produced with one or more Tool failures, THE Agent SHALL include a "Failed Tools" section listing each failed Tool's name and associated error message.
3. WHILE at least one Tool call has succeeded, THE Agent SHALL complete and return the Investigation_Summary within 5 seconds of the last Tool call completing.
4. IF all Tool calls fail, THEN THE Agent SHALL return an Investigation_Summary with Confidence_Level "Low", an empty Evidence list, and a Recommended_Actions entry directing the user to verify tool connectivity.
5. WHEN a Tool call fails, THE Agent SHALL capture and store the tool name, failure timestamp, and error message or timeout indicator as failure metadata for inclusion in the "Failed Tools" section.

---

### Requirement 9: Complete Investigation Within Acceptable Time

**User Story:** As an on-call engineer, I want the investigation to complete within a reasonable time so that I receive findings while the incident is still active.

#### Acceptance Criteria

1. WHEN an investigation is initiated, THE Agent SHALL complete the investigation and return the Investigation_Summary within 30 seconds, measured from the moment the investigation starts to the moment the Investigation_Summary is available to the caller.
2. THE Agent SHALL call all tools concurrently where the calls are independent of each other, to minimize total investigation time.
3. IF a single Tool call does not respond within 10 seconds, THEN THE Agent SHALL treat the call as failed, record a timeout error for that tool in the Evidence list, and continue processing results from any remaining tools.
4. IF the total investigation duration reaches 30 seconds before all Tool calls have completed, THEN THE Agent SHALL cancel any outstanding Tool calls, record a timeout error for each unresolved tool in the Evidence list, and return the Investigation_Summary with the results collected up to that point.
