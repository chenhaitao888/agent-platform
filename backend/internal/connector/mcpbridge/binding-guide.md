Binding JSON schema (all fields required unless marked optional):

schemaVersion: "mcp-company-binding.v1", version: operator chosen version (start "v1"),
skillHash and toolsDigest: exact supplied values,
combinedSaveSubmit: true if save atomically creates the company work order,
read, save, lookupSave, ticket, publication, published: Call objects.
submit and lookupSubmit: Call objects required only if combinedSaveSubmit=false; omit otherwise.
confirmation: OMIT. Only the local operator may confirm a reviewed proposal.

Call: {tool, toolHash, arguments: JSON template, result: JSON template,
resultEncoding: "structured" or "json-text"}.
toolHash is the supplied tool hash. json-text requires exactly one text block containing an entire JSON value.

Templates: ordinary JSON objects/arrays/scalars are literals. {"$path":"/input/key"}
reads a JSON Pointer. {"$path":"/result/status","$map":{"wait":"PENDING","ok":"APPROVED"}}
maps exact enum strings (unknown values fail). Optional $as: "string" converts a
scalar to text, "number"/"boolean" parse canonical strings, "json" parses a JSON string.
{"$each":"/result/items","template":{...}} maps an array, exposing its element as /item.
No scripts, interpolation, optional-path defaults, regex extraction or arbitrary calls.
To choose create versus update arguments, use {"$switch":"/input/operation",
"cases":{"config.create":{...},"config.update":{...}}}; only the selected template
is evaluated, so an absent expectedVersion on create is not accessed.
Missing paths fail. Do not map approval or success states to constant values.

Trusted Target: {tenantId,environment,connectorRef,resourceRef}.
Value: {type:"string"|"boolean"|"integer"|"number",text:string}, or
{type:"secretRef",secretRef,secretVersion}. Do not read or return secret plaintext.
The typed value's text represents its value, e.g. "true", "15". Target and fields
needed for provider routing are from /input, never hardcode another environment.

read input: {target}. Normalized result:
{target,version,saveMode:"publish_required",conditionalUpdates:true,
 items:[{key,version,value:Value,constraints:{}}]}. Optional item sensitive:boolean,
constraints:{min?,max?,allowed?:string[]}, secretReferences:[{ref,versions:string[]}].
Read the saved/draft resource (published values are queried separately).
Only attest conditionalUpdates if save enforces resource CAS and item version or absence atomically.

save / lookupSave input: Intent containing operationId, planHash, actionId,
actionHash, hash, target, key, operation (config.create/config.update),
resourceVersion (expected resource CAS), expectedVersion (absent for create),
mustNotExist, value:Value, expiresAt, effect:"publish_required", and descriptors.
Use operationId as stable idempotency/request key, enforce resourceVersion and
expectedVersion OR mustNotExist; expiry must be enforced by provider. The lookup
must retrieve the SAME immutable request result after a dropped response, without writes.
Normalized SaveResult:
{id,requestId,target,key,status:"APPLIED"|"REJECTED",recordedAt:ISO8601,
 beforeVersion?,resourceVersion?,itemVersion?,value?:Value,
 change?:{id,version,ticket?:Ticket},code?:string}.
APPLIED requires all versions, value, change; requestId must equal operationId.
REJECTED uses code precondition_failed/intent_expired/unsupported/value_rejected.
BeforeVersion must match expected resourceVersion; new versions must differ on mutation.
RecordedAt must be actual provider commit time and no later than expiresAt.
For combinedSaveSubmit, change.ticket is mandatory and Ticket.requestId=operationId.
For separate submission omit change.ticket. Do not invent provider evidence by copying
input value/version into a result if the provider does not prove its saved content.

submit / lookupSubmit input: {intent:Intent,receipt:Receipt,requestId}.
Receipt includes company:{id,version}, id, resourceVersion, itemVersion, target,
key, valueHash, recordedAt. Submit exactly the saved change/version and target,
with stable requestId and intent.expiresAt. Lookup returns the same work order.
Normalized result is Ticket:
{id,requestId,changeId,changeVersion,target,state:"PENDING"|"APPROVED"|"REJECTED"|"WITHDRAWN"|"EXPIRED",url?:string}.
The company system, never this platform/model, supplies the approval conclusion.
This version submits one work order per configuration action. If the platform
only supports grouped work orders, explain that capability gap in questions.

ticket input: {intent,receipt,ticket:Ticket}. Result: current Ticket with exact
requestId, id, changeId/changeVersion and target still matching the submitted work order.

publication input: {intent,receipt,ticket:Ticket}. Result:
{id,ticketId,changeId,changeVersion,target,state:"PENDING"|"PUBLISHING"|"PUBLISHED"|"FAILED",
 effectiveVersion?:string}. PUBLISHED requires publication id and effectiveVersion.
Company platform automatically publishes; DO NOT bind/call a publish/approve tool.

published input: {intent,receipt,ticket?,publication?}. For config.no_change no
ticket or publication exists, so route using intent.target and intent.key.
Result: {target,key,version,value:Value}, obtained from the actually published
test environment. version must equal publication.effectiveVersion after publication.
Do not use saved/draft values for this query or fabricate an effective version.

Identifiers/versions must be 1..128 characters, begin alphanumeric and contain
only alphanumeric plus . _ : / -, with no .. or ://. Numeric IDs can use $as:string.
If the company API cannot meet these contracts, leave questions; do not claim readiness.
