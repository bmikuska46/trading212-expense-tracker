import { z } from "zod";
import { translator } from "@/i18n/translate";

const runtimeSchema = z.object({
  status: z.literal("ok"),
  database: z.literal("encrypted"),
  cipherVersion: z.string().min(1),
  createdAt: z.iso.datetime(),
});

const userSchema = z.object({ email: z.string(), timezone: z.string(), owner: z.boolean(), mustChangePassword: z.boolean() });

const sessionSchema = z.object({
  turnstileSiteKey: z.string(),
  setupRequired: z.boolean().default(false),
  authenticated: z.boolean(),
  user: userSchema.nullable(),
});

const userEntrySchema = z.object({
  id: z.number(),
  email: z.string(),
  owner: z.boolean(),
  mustChangePassword: z.boolean(),
  createdAt: z.string(),
});

const signInSchema = z.object({
  createdAt: z.string(),
  ip: z.string(),
  country: z.string(),
  region: z.string(),
  city: z.string(),
  userAgent: z.string(),
  current: z.boolean(),
});

const checkSchema = z.object({
  status: z.enum(["ok", "partial", "failed"]),
  message: z.string(),
  items: z.array(z.object({
    key: z.string(),
    label: z.string(),
    state: z.enum(["ok", "missing", "unverified"]),
    detail: z.string().optional(),
  })),
});

const trading212SyncSchema = z.object({
  portfolio: z.boolean(),
  pies: z.boolean(),
  dividends: z.boolean(),
  interest: z.boolean(),
  cashback: z.boolean(),
});
export type Trading212Sync = z.infer<typeof trading212SyncSchema>;

const integrationSchema = z.object({
  provider: z.enum(["trading212", "typesafe", "resend"]),
  configured: z.boolean(),
  enabled: z.boolean(),
  apiKeyHint: z.string(),
  apiSecretConfigured: z.boolean(),
  environment: z.enum(["live", "demo"]).optional(),
  account: z.object({ id: z.number(), name: z.string(), currency: z.string() }).nullable(),
  earliestAutomaticPeriod: z.string(),
  sender: z.string(),
  recipient: z.string(),
  applicationUrl: z.string(),
  check: checkSchema.nullable(),
  checkedAt: z.string(),
  categorize: z.boolean(),
  sync: trading212SyncSchema.optional(),
});

const accountSchema = z.object({
  id: z.number(),
  name: z.string(),
  currency: z.string(),
  provider: z.string(),
  archived: z.boolean(),
  balance: z.string(),
  balanceEurCents: z.number(),
  transactionCount: z.number(),
  createdAt: z.string(),
});

const kindSchema = z.enum(["expense", "income", "transfer", "refund"]);
const reviewSchema = z.enum(["needs_review", "confirmed", "source_conflict"]);

const transactionSchema = z.object({
  id: z.number(),
  accountId: z.number(),
  accountName: z.string(),
  date: z.string(),
  kind: kindSchema,
  amount: z.string(),
  currency: z.string(),
  eurCents: z.number(),
  notes: z.string(),
  reviewStatus: reviewSchema,
  revision: z.number(),
  payeeId: z.number().nullable(),
  payeeName: z.string(),
  categoryId: z.number().nullable(),
  categoryName: z.string(),
  splitCount: z.number(),
  refundOf: z.number().nullable(),
  transferAccountName: z.string(),
  imported: z.boolean(),
});

const registerPageSchema = z.object({
  transactions: z.array(transactionSchema),
  total: z.number(),
  page: z.number(),
  pageSize: z.number(),
});

const transactionDetailSchema = transactionSchema.extend({
  occurredAt: z.string(),
  splits: z.array(z.object({ categoryId: z.number(), categoryName: z.string(), amount: z.string(), eurCents: z.number(), notes: z.string() })),
  refunds: z.array(transactionSchema),
  refundedTotal: z.string(),
  refundOfTransaction: transactionSchema.nullable(),
  transferCounterpart: transactionSchema.nullable(),
  importedOriginal: z.object({
    amount: z.string(),
    currency: z.string(),
    timestamp: z.string(),
    action: z.string(),
    merchantName: z.string(),
    merchantCategory: z.string(),
    notes: z.string(),
    feeAmount: z.string().nullable(),
    feeCurrency: z.string().nullable(),
  }).nullable(),
  conversion: z.object({
    originalAmount: z.string(),
    originalCurrency: z.string(),
    eurCents: z.number(),
    rate: z.string(),
    rateDate: z.string(),
    providerPolicy: z.string(),
    roundingRule: z.string(),
    convertedAt: z.string(),
  }).nullable(),
  editCount: z.number(),
  importBatch: z.object({ id: z.number(), reviewStatus: z.string() }).nullable(),
});

const ruleSuggestionSchema = z.object({
  payeeId: z.number(),
  payeeName: z.string(),
  categoryId: z.number(),
  categoryName: z.string(),
  otherTransactions: z.number(),
});

const statisticsSchema = z.object({
  incomeEurCents: z.number(),
  expenseEurCents: z.number(),
  refundEurCents: z.number(),
  spendingEurCents: z.number(),
  netEurCents: z.number(),
  transferCount: z.number(),
  categories: z.array(z.object({
    categoryId: z.number().nullable(),
    categoryName: z.string(),
    spendingEurCents: z.number(),
    incomeEurCents: z.number(),
  })),
});

const dashboardSchema = z.object({
  month: z.string(),
  from: z.string(),
  to: z.string(),
  accountId: z.number().nullable(),
  incomeEurCents: z.number(),
  expenseEurCents: z.number(),
  refundEurCents: z.number(),
  spendingEurCents: z.number(),
  netEurCents: z.number(),
  transferCount: z.number(),
  categories: z.array(z.object({
    categoryId: z.number().nullable(),
    categoryName: z.string(),
    spendingEurCents: z.number(),
    other: z.boolean(),
  })),
  trend: z.array(z.object({
    month: z.string(),
    incomeEurCents: z.number(),
    spendingEurCents: z.number(),
    netEurCents: z.number(),
  })),
  accounts: z.array(z.object({
    id: z.number(),
    name: z.string(),
    currency: z.string(),
    archived: z.boolean(),
    balance: z.string(),
    balanceEurCents: z.number(),
    change: z.string(),
    changeEurCents: z.number(),
    transactionCount: z.number(),
  })),
  review: z.object({
    importCount: z.number(),
    imports: z.array(z.object({
      id: z.number(),
      filename: z.string(),
      account: z.object({ id: z.number(), name: z.string(), currency: z.string() }),
      insertedCount: z.number(),
      periodStart: z.string().nullable(),
      periodEnd: z.string().nullable(),
      committedAt: z.string().nullable(),
    })),
    needsReviewCount: z.number(),
    conflictCount: z.number(),
  }),
});

const subscriptionsSchema = z.object({
  today: z.string(),
  monthlyEurCents: z.number(),
  yearlyEurCents: z.number(),
  subscriptions: z.array(z.object({
    payeeId: z.number(),
    payeeName: z.string(),
    categoryId: z.number().nullable(),
    categoryName: z.string(),
    account: z.object({ id: z.number(), name: z.string(), currency: z.string() }),
    amount: z.string(),
    currency: z.string(),
    eurCents: z.number(),
    previousAmount: z.string().nullable(),
    months: z.number(),
    paidEurCents: z.number(),
    since: z.string(),
    lastPaid: z.string(),
    nextDue: z.string(),
    active: z.boolean(),
  })),
});

const entitySchema = z.object({
  id: z.number(),
  name: z.string(),
  transactionCount: z.number(),
  aliases: z.array(z.string()),
  system: z.boolean().default(false),
  ruleId: z.number().optional(),
  ruleCategoryId: z.number().optional(),
  ruleCategoryName: z.string().optional(),
});

const ruleSchema = z.object({
  id: z.number(),
  payeeId: z.number(),
  payeeName: z.string(),
  categoryId: z.number(),
  categoryName: z.string(),
  enabled: z.boolean(),
});

// The server validates mappings, so enumerated fields stay plain strings here
// and an unexpected value shows up in the editor instead of failing the parse.
const columnSchema = z.number().int().nullable();

const mappingSchema = z.object({
  date: z.object({ column: z.number().int(), format: z.string(), timezone: z.string() }),
  amount: z.object({
    mode: z.string(),
    column: columnSchema,
    debitColumn: columnSchema,
    creditColumn: columnSchema,
    sign: z.string(),
    decimalSeparator: z.string(),
    thousandsSeparator: z.string(),
  }),
  currency: z.object({ column: columnSchema, fixed: z.string().nullable() }),
  externalIdColumn: columnSchema,
  payeeColumn: columnSchema,
  categoryColumn: columnSchema,
  notesColumn: columnSchema,
  notesFallbackColumn: columnSchema.optional().default(null),
  actionColumn: columnSchema,
  feeAmountColumn: columnSchema,
  feeCurrencyColumn: columnSchema,
  actions: z.array(z.object({ value: z.string(), kind: z.string() })).nullable().transform((value) => value ?? []),
  ambiguities: z.array(z.string()).nullable().transform((value) => value ?? []),
});

const importStateSchema = z.enum(["uploading", "failed", "needs_mapping", "needs_validation", "needs_attention", "ready", "committed"]);
const importReviewSchema = z.enum(["not_required", "needs_review", "confirmed"]);

const importSchema = z.object({
  id: z.number(),
  account: z.object({ id: z.number(), name: z.string(), currency: z.string() }),
  filename: z.string(),
  state: importStateSchema,
  periodStart: z.string().nullable(),
  periodEnd: z.string().nullable(),
  rowCount: z.number(),
  insertedCount: z.number(),
  excludedCount: z.number(),
  duplicateCount: z.number(),
  errorCount: z.number(),
  conflictCount: z.number(),
  validationErrors: z.array(z.object({ row: z.number(), message: z.string() })),
  unknownActions: z.array(z.object({ value: z.string(), rows: z.number() })),
  mappingVersion: z.number().nullable(),
  createdAt: z.string(),
  committedAt: z.string().nullable(),
  reviewStatus: importReviewSchema,
  confirmedAt: z.string().nullable(),
});

const importDetailSchema = importSchema.extend({
  header: z.array(z.string()),
  examples: z.array(z.string()),
  mapping: mappingSchema.nullable(),
  rowCounts: z.record(z.string(), z.number()),
  canSuggest: z.boolean(),
  suggestedCategoryCount: z.number(),
  scheduledPeriod: z.string().nullable(),
});

const rowStatusSchema = z.enum(["pending", "valid", "invalid", "excluded", "duplicate", "conflict"]);

const stagingRowSchema = z.object({
  row: z.number(),
  values: z.array(z.string()),
  correctedValues: z.array(z.string()).nullable(),
  excluded: z.boolean(),
  status: rowStatusSchema,
  errors: z.array(z.string()),
  record: z.object({
    date: z.string(),
    kind: z.string(),
    amount: z.string(),
    currency: z.string(),
    payee: z.string(),
    category: z.string(),
    notes: z.string(),
    action: z.string(),
    externalId: z.string(),
    conversion: z.object({
      rate: z.string(),
      rateDate: z.string(),
      providerPolicy: z.string(),
      roundingRule: z.string(),
      eurCents: z.number(),
    }).optional(),
  }).nullable(),
});

const ratesStatusSchema = z.object({
  endpoint: z.string(),
  bases: z.array(z.string()),
  providerPolicy: z.string(),
  timezone: z.string(),
  currencies: z.number(),
  latestPublication: z.string().nullable(),
  lastFetchedAt: z.string().nullable(),
  nextRunAt: z.string(),
  lastAttempt: z.object({
    stage: z.string(),
    startedAt: z.string(),
    finishedAt: z.string().nullable(),
    outcome: z.string().nullable(),
    error: z.string().nullable(),
  }).nullable(),
});

const scheduledJobSchema = z.object({
  id: z.number(),
  period: z.string(),
  state: z.string(),
  reportStatus: z.string().nullable(),
  batchId: z.number().nullable(),
  retryCount: z.number(),
  nextAttemptAt: z.string(),
  errorCode: z.string().nullable(),
  errorDetail: z.string().nullable(),
  updatedAt: z.string(),
  canRerun: z.boolean(),
});

const scheduledImportsSchema = z.object({
  enabled: z.boolean(),
  accountId: z.number().nullable(),
  earliestPeriod: z.string(),
  timezone: z.string(),
  nextRunAt: z.string(),
  jobs: z.array(scheduledJobSchema),
});

const notificationSchema = z.object({
  id: z.number(),
  kind: z.enum(["import_review", "import_failure"]),
  source: z.string(),
  status: z.string(),
  reason: z.string(),
  newTransactions: z.number(),
  batchId: z.number().nullable(),
  link: z.string(),
  deliveryState: z.enum(["pending", "sending", "delivered", "failed", "uncertain"]),
  deliveryError: z.string().nullable(),
  createdAt: z.string(),
  canDismiss: z.boolean(),
  canResend: z.boolean(),
});

const investingRangeSchema = z.enum(["7d", "30d", "90d", "1y", "all"]);

const investingGroupSchema = z.object({ id: z.number(), name: z.string(), pieIds: z.array(z.number()) });

// Amounts are minor units of the snapshot currency.
const investingSchema = z.object({
  configured: z.boolean(),
  range: investingRangeSchema,
  currency: z.string().nullable(),
  startedOn: z.string().nullable(),
  lastAttemptAt: z.string().nullable(),
  lastError: z.string().nullable(),
  snapshots: z.array(z.object({
    at: z.string(),
    totalValueMinor: z.number(),
    investmentsValueMinor: z.number(),
    costBasisMinor: z.number(),
    unrealizedMinor: z.number(),
    realizedMinor: z.number(),
    cashAvailableMinor: z.number(),
    cashInPiesMinor: z.number(),
    cashReservedMinor: z.number(),
    pies: z.array(z.object({
      id: z.number(),
      investedMinor: z.number(),
      valueMinor: z.number(),
      resultMinor: z.number(),
      cashMinor: z.number(),
      dividendsGainedMinor: z.number(),
      dividendsReinvestedMinor: z.number(),
      dividendsInCashMinor: z.number(),
    })),
  })),
  pies: z.array(z.object({ id: z.number(), name: z.string().nullable() })),
  groups: z.array(investingGroupSchema),
});

const incomeMethodSchema = z.enum(["monthly", "daily", "annual", "income_months"]);
const incomeKindSchema = z.enum(["dividend", "interest", "cashback"]);
// The percentage the income page taxes each kind at.
const taxSettingsSchema = z.object({ dividend: z.number(), interest: z.number(), cashback: z.number() });

// Amounts are minor units of the item currency; conversions and totals are
// EUR cents. A conversion without a rate has null fields.
const incomeSchema = z.object({
  configured: z.boolean(),
  year: z.number(),
  years: z.array(z.number()),
  sync: z.object({
    startedOn: z.string().nullable(),
    dividendsSyncedAt: z.string().nullable(),
    interestSyncedAt: z.string().nullable(),
    historyAttemptAt: z.string().nullable(),
    historyError: z.string().nullable(),
    cashbackThrough: z.string().nullable(),
    cashbackAttemptAt: z.string().nullable(),
    cashbackError: z.string().nullable(),
    cashbackPending: z.boolean(),
    ratesCheckedAt: z.string().nullable(),
    ratesError: z.string().nullable(),
  }),
  taxPercent: z.record(incomeKindSchema, z.number()),
  items: z.array(z.object({
    id: z.number(),
    kind: incomeKindSchema,
    type: z.string(),
    paidAt: z.string(),
    paidOn: z.string(),
    currency: z.string(),
    amountMinor: z.number(),
    receivedCurrency: z.string(),
    receivedMinor: z.number(),
    ticker: z.string().nullable(),
    isin: z.string().nullable(),
    name: z.string().nullable(),
    quantity: z.string().nullable(),
    perShare: z.string().nullable(),
    taxable: z.boolean(),
    informativeEurCents: z.number().nullable(),
    informativeRate: z.string().nullable(),
    informativeRateDate: z.string().nullable(),
    conversions: z.record(incomeMethodSchema, z.object({
      rate: z.string().nullable(),
      period: z.string().nullable(),
      eurCents: z.number().nullable(),
      provisional: z.boolean(),
    })),
  })),
  totals: z.array(z.object({
    method: incomeMethodSchema,
    bases: z.partialRecord(incomeKindSchema, z.number()),
    taxes: z.partialRecord(incomeKindSchema, z.number()),
    baseCents: z.number(),
    taxCents: z.number(),
    missing: z.number(),
    provisional: z.boolean(),
  })),
  recommended: incomeMethodSchema.nullable(),
  equal: z.boolean(),
  months: z.record(z.string(), z.array(z.string())),
});

// Chat answers come from fixed queries on the server. Money cells are minor
// units of the column currency, EUR cents unless the column names another.
const chatColumnSchema = z.object({
  label: z.string(),
  kind: z.enum(["text", "money", "count", "percent", "date"]),
  currency: z.string().optional(),
  bar: z.boolean().optional(),
});

const chatAnswerSchema = z.object({
  text: z.string(),
  table: z.object({
    columns: z.array(chatColumnSchema),
    rows: z.array(z.array(z.union([z.string(), z.number(), z.null()]))),
  }).optional(),
  notes: z.array(z.string()).optional(),
  link: z.object({ label: z.string(), href: z.string() }).optional(),
  suggestions: z.array(z.string()).optional(),
});

const chatSchema = z.object({ id: z.number(), title: z.string(), createdAt: z.string(), updatedAt: z.string() });

const chatMessageSchema = z.object({ id: z.number(), question: z.string(), answer: chatAnswerSchema, createdAt: z.string() });

const chatListSchema = z.object({
  chats: z.array(chatSchema),
  examples: z.array(z.object({ title: z.string(), questions: z.array(z.string()) })),
});

const chatDetailSchema = chatSchema.extend({ messages: z.array(chatMessageSchema) });

const chatReplySchema = z.object({ chat: chatSchema, message: chatMessageSchema });

const errorSchema = z.object({
  error: z.string(),
  fields: z.record(z.string(), z.string()).optional(),
  check: checkSchema.optional(),
});

export type Session = z.infer<typeof sessionSchema>;
export type User = z.infer<typeof userSchema>;
export type UserEntry = z.infer<typeof userEntrySchema>;
export type SignIn = z.infer<typeof signInSchema>;
export type Check = z.infer<typeof checkSchema>;
export type Integration = z.infer<typeof integrationSchema>;
export type Provider = Integration["provider"];
export type Account = z.infer<typeof accountSchema>;
export type Kind = z.infer<typeof kindSchema>;
export type ReviewStatus = z.infer<typeof reviewSchema>;
export type Transaction = z.infer<typeof transactionSchema>;
export type RegisterPage = z.infer<typeof registerPageSchema>;
export type TransactionDetail = z.infer<typeof transactionDetailSchema>;
export type RuleSuggestion = z.infer<typeof ruleSuggestionSchema>;
export type Statistics = z.infer<typeof statisticsSchema>;
export type Dashboard = z.infer<typeof dashboardSchema>;
export type Subscriptions = z.infer<typeof subscriptionsSchema>;
export type Entity = z.infer<typeof entitySchema>;
export type Rule = z.infer<typeof ruleSchema>;
export type Mapping = z.infer<typeof mappingSchema>;
export type ImportState = z.infer<typeof importStateSchema>;
export type Import = z.infer<typeof importSchema>;
export type ImportDetail = z.infer<typeof importDetailSchema>;
export type ImportReviewStatus = z.infer<typeof importReviewSchema>;
export type ScheduledJob = z.infer<typeof scheduledJobSchema>;
export type ScheduledImports = z.infer<typeof scheduledImportsSchema>;
export type Notification = z.infer<typeof notificationSchema>;
export type RowStatus = z.infer<typeof rowStatusSchema>;
export type StagingRow = z.infer<typeof stagingRowSchema>;
export type RatesStatus = z.infer<typeof ratesStatusSchema>;
export type TaxSettings = z.infer<typeof taxSettingsSchema>;
export type InvestingRange = z.infer<typeof investingRangeSchema>;
export type Investing = z.infer<typeof investingSchema>;
export type InvestingGroup = z.infer<typeof investingGroupSchema>;
export type InvestmentIncome = z.infer<typeof incomeSchema>;
export type IncomeMethod = z.infer<typeof incomeMethodSchema>;
export type IncomeKind = z.infer<typeof incomeKindSchema>;
export type Chat = z.infer<typeof chatSchema>;
export type ChatColumn = z.infer<typeof chatColumnSchema>;
export type ChatAnswer = z.infer<typeof chatAnswerSchema>;
export type ChatMessage = z.infer<typeof chatMessageSchema>;
export type ChatList = z.infer<typeof chatListSchema>;
export type ChatDetail = z.infer<typeof chatDetailSchema>;
export type ChatReply = z.infer<typeof chatReplySchema>;

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly fields: Record<string, string> = {},
    readonly check?: Check,
  ) {
    super(message);
  }
}

type Method = "GET" | "POST" | "PUT" | "PATCH" | "DELETE";

async function parseResponse<T>(schema: z.ZodType<T>, response: Response): Promise<T> {
  if (response.status === 204) return schema.parse(undefined);
  const payload: unknown = await response.json().catch(() => undefined);
  if (!response.ok) {
    const parsed = errorSchema.safeParse(payload);
    throw new ApiError(parsed.success ? parsed.data.error : translator()("common.errors.request"), response.status,
      parsed.success ? parsed.data.fields : undefined, parsed.success ? parsed.data.check : undefined);
  }
  const result = schema.safeParse(payload);
  if (!result.success) throw new ApiError(translator()("common.errors.response"), response.status);
  return result.data;
}

const unavailable = () => translator()("common.errors.unavailable");

async function request<T>(schema: z.ZodType<T>, method: Method, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  let response: Response;
  try {
    response = await fetch(path, {
      method, signal, credentials: "same-origin", cache: "no-store",
      headers: body === undefined ? undefined : { "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch (error) {
    if (error instanceof DOMException && error.name === "AbortError") throw error;
    throw new ApiError(unavailable(), 0);
  }
  return parseResponse(schema, response);
}

type Options = { signal?: AbortSignal };

export const getRuntime = ({ signal }: Options) => request(runtimeSchema, "GET", "/api/health", undefined, signal);
export const getSession = ({ signal }: Options) => request(sessionSchema, "GET", "/api/session", undefined, signal);
export const setUp = (input: { email: string; password: string; timezone: string }) =>
  request(sessionSchema, "POST", "/api/setup", input);
export const signIn = (input: { email: string; password: string; turnstileToken: string }) => request(sessionSchema, "POST", "/api/session", input);
export const signOut = () => request(z.undefined(), "DELETE", "/api/session");

export const updateTimezone = (timezone: string) => request(userSchema, "PATCH", "/api/settings", { timezone });
export const getTaxSettings = ({ signal }: Options) => request(taxSettingsSchema, "GET", "/api/settings/tax", undefined, signal);
export const updateTaxSettings = (input: TaxSettings) => request(taxSettingsSchema, "PUT", "/api/settings/tax", input);
export const changePassword = (input: { currentPassword: string; newPassword: string }) =>
  request(sessionSchema, "POST", "/api/password", input);

export const listSessions = ({ signal }: Options) =>
  request(z.object({ sessions: z.array(signInSchema) }), "GET", "/api/sessions", undefined, signal).then((body) => body.sessions);

export const listUsers = ({ signal }: Options) =>
  request(z.object({ users: z.array(userEntrySchema) }), "GET", "/api/users", undefined, signal).then((body) => body.users);
export const createUser = (input: { email: string; password: string }) => request(userEntrySchema, "POST", "/api/users", input);
export const resetUserPassword = (id: number, password: string) =>
  request(userEntrySchema, "PUT", `/api/users/${id}/password`, { password });
export const deleteUser = (id: number) => request(z.undefined(), "DELETE", `/api/users/${id}`);

export const listIntegrations = ({ signal }: Options) =>
  request(z.object({ integrations: z.array(integrationSchema) }), "GET", "/api/integrations", undefined, signal)
    .then((body) => body.integrations);
export type Trading212Input = {
  environment: "live" | "demo"; apiKey: string; apiSecret: string; accountName: string; enabled: boolean; sync: Trading212Sync;
};
export type TypeSafeInput = { apiKey: string; enabled: boolean; categorize: boolean };
export type ResendInput = { apiKey: string; sender: string; recipient: string; applicationUrl: string; enabled: boolean };
export function saveIntegration(provider: "trading212", input: Trading212Input): Promise<Integration>;
export function saveIntegration(provider: "typesafe", input: TypeSafeInput): Promise<Integration>;
export function saveIntegration(provider: "resend", input: ResendInput): Promise<Integration>;
export function saveIntegration(provider: Provider, input: object) {
  return request(integrationSchema, "PUT", `/api/integrations/${provider}`, input);
}
export const checkIntegration = (provider: Provider) => request(integrationSchema, "POST", `/api/integrations/${provider}/check`);
export const removeIntegration = (provider: Provider) => request(integrationSchema, "DELETE", `/api/integrations/${provider}`);

export const listCurrencies = ({ signal }: Options) =>
  request(z.object({ currencies: z.array(z.string()) }), "GET", "/api/currencies", undefined, signal).then((body) => body.currencies);
export const getRates = ({ signal }: Options) => request(ratesStatusSchema, "GET", "/api/rates", undefined, signal);
export const listAccounts = ({ signal }: Options) =>
  request(z.object({ accounts: z.array(accountSchema) }), "GET", "/api/accounts", undefined, signal).then((body) => body.accounts);
export const createAccount = (input: { name: string; currency: string }) => request(accountSchema, "POST", "/api/accounts", input);

// RegisterQuery mirrors the server's register filters. Empty values are
// omitted so the address and the query key stay short.
export type RegisterQuery = {
  q?: string; from?: string; to?: string; account?: string; category?: string; payee?: string; batch?: string;
  kind?: string; review?: string; sort?: string; dir?: string; page?: string; pageSize?: string;
};

export function registerSearch(query: Record<string, string | undefined>): string {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) if (value) params.set(key, value);
  const text = params.toString();
  return text ? `?${text}` : "";
}

export const listTransactions = (query: RegisterQuery, { signal }: Options) =>
  request(registerPageSchema, "GET", `/api/transactions${registerSearch(query)}`, undefined, signal);
export const getStatistics = (query: RegisterQuery, { signal }: Options) =>
  request(statisticsSchema, "GET", `/api/statistics${registerSearch(query)}`, undefined, signal);
export type DashboardQuery = { month?: string; account?: string };
export const getDashboard = (query: DashboardQuery, { signal }: Options) =>
  request(dashboardSchema, "GET", `/api/dashboard${registerSearch(query)}`, undefined, signal);
export const getSubscriptions = ({ signal }: Options) =>
  request(subscriptionsSchema, "GET", "/api/subscriptions", undefined, signal);
export const getTransaction = (id: number, { signal }: Options) =>
  request(transactionDetailSchema, "GET", `/api/transactions/${id}`, undefined, signal);

export type SplitInput = { categoryId: number; amount: string; notes: string };
export type TransactionInput = {
  revision?: number; accountId: number; toAccountId?: number; date: string; kind: Kind; amount: string; payee: string;
  categoryId: number | null; refundOf: number | null; notes: string; splits: SplitInput[];
  confirmImport?: boolean;
};
export const createTransaction = (input: TransactionInput) => request(transactionSchema, "POST", "/api/transactions", input);
export const updateTransaction = (id: number, input: TransactionInput) =>
  request(z.object({ transaction: transactionSchema, ruleSuggestion: ruleSuggestionSchema.nullable() }), "PATCH", `/api/transactions/${id}`, input);
export const deleteTransaction = (id: number, revision: number) =>
  request(z.undefined(), "DELETE", `/api/transactions/${id}?revision=${revision}`);

export type BulkInput = {
  items: { id: number; revision: number }[];
  action: "update" | "delete";
  categoryId?: number; payee?: string; accountId?: number; reviewed?: boolean;
};
export const bulkTransactions = (input: BulkInput) =>
  request(z.object({ changed: z.number() }), "POST", "/api/transactions/bulk", input).then((body) => body.changed);
export const categorizeTransactions = () =>
  request(z.object({ categorized: z.number(), remaining: z.number() }), "POST", "/api/transactions/categorize");

export type EntityKind = "categories" | "payees";
export const listCategories = ({ signal }: Options) =>
  request(z.object({ categories: z.array(entitySchema) }), "GET", "/api/categories", undefined, signal).then((body) => body.categories);
export type PayeeSort = "name" | "rule" | "transactionCount";
export const listPayees = (query: { q?: string; page?: number; pageSize?: number; sort?: PayeeSort; dir?: "asc" | "desc" }, { signal }: Options) =>
  request(z.object({ payees: z.array(entitySchema), total: z.number(), page: z.number(), pageSize: z.number() }), "GET",
    `/api/payees${registerSearch({ q: query.q, page: query.page ? String(query.page) : undefined, pageSize: query.pageSize ? String(query.pageSize) : undefined,
      sort: query.sort, dir: query.dir })}`,
    undefined, signal);
export const createEntity = (kind: EntityKind, name: string) => request(entitySchema, "POST", `/api/${kind}`, { name });
export const renameEntity = (kind: EntityKind, id: number, name: string) => request(entitySchema, "PATCH", `/api/${kind}/${id}`, { name });
export const mergeEntity = (kind: EntityKind, id: number, targetId: number) =>
  request(z.undefined(), "POST", `/api/${kind}/${id}/merge`, { targetId });

export const listRules = ({ signal }: Options) =>
  request(z.object({ rules: z.array(ruleSchema) }), "GET", "/api/rules", undefined, signal).then((body) => body.rules);
export const saveRule = (input: { payeeId?: number; payee?: string; categoryId?: number; category?: string }) =>
  request(z.object({ id: z.number() }), "POST", "/api/rules", input);
export const updateRule = (id: number, input: { categoryId: number; enabled: boolean }) =>
  request(z.undefined(), "PATCH", `/api/rules/${id}`, input);
export const deleteRule = (id: number) => request(z.undefined(), "DELETE", `/api/rules/${id}`);

// uploadImport streams the file as the raw request body. It is the one call
// that sends CSV instead of JSON, so the browser never builds a multipart form.
export async function uploadImport(input: { accountId: number; file: File }): Promise<ImportDetail> {
  const params = new URLSearchParams({ accountId: String(input.accountId), filename: input.file.name });
  let response: Response;
  try {
    response = await fetch(`/api/imports?${params.toString()}`, {
      method: "POST", credentials: "same-origin", cache: "no-store",
      headers: { "Content-Type": "text/csv; charset=utf-8" }, body: input.file,
    });
  } catch {
    throw new ApiError(unavailable(), 0);
  }
  return parseResponse(importDetailSchema, response);
}
export type ImportSort = "filename" | "account" | "state" | "period" | "rowCount" | "insertedCount" | "duplicateCount" | "problems" | "createdAt";
export const listImports = (query: { page: number; sort?: ImportSort; dir?: "asc" | "desc" }, { signal }: Options) =>
  request(z.object({ imports: z.array(importSchema), total: z.number(), page: z.number(), pageSize: z.number() }), "GET",
    `/api/imports${registerSearch({ page: query.page > 1 ? String(query.page) : undefined, sort: query.sort, dir: query.dir })}`, undefined, signal);
export const getImport = (id: number, { signal }: Options) => request(importDetailSchema, "GET", `/api/imports/${id}`, undefined, signal);
export const listImportRows = (id: number, query: { status?: RowStatus; after?: number }, { signal }: Options) =>
  request(z.object({ rows: z.array(stagingRowSchema), nextAfter: z.number().nullable() }), "GET",
    `/api/imports/${id}/rows${registerSearch({ status: query.status, after: query.after ? String(query.after) : undefined })}`,
    undefined, signal);
export const setImportMapping = (id: number, mapping: Mapping) => request(importDetailSchema, "PUT", `/api/imports/${id}/mapping`, { mapping });
// A suggestion fills the mapping form. Notes are answers Jev was unsure of,
// a date order the sample cannot show, and sample rows the mapping could not
// read, for the owner to check.
export type MappingSuggestion = { mapping: Mapping; notes: string[] };
export const suggestImportMapping = (id: number): Promise<MappingSuggestion> =>
  request(z.object({ mapping: mappingSchema, notes: z.array(z.string()) }), "POST", `/api/imports/${id}/suggest-mapping`);
export const validateImport = (id: number) => request(importDetailSchema, "POST", `/api/imports/${id}/validate`);
export const commitImport = (id: number) => request(importDetailSchema, "POST", `/api/imports/${id}/commit`);
export const correctImportRow = (id: number, row: number, input: { excluded: boolean; values: string[] | null }) =>
  request(importDetailSchema, "PATCH", `/api/imports/${id}/rows/${row}`, input);
export const deleteImport = (id: number) => request(z.undefined(), "DELETE", `/api/imports/${id}`);
export const confirmImport = (id: number) => request(importDetailSchema, "POST", `/api/imports/${id}/confirm`);

export const getScheduledImports = ({ signal }: Options) =>
  request(scheduledImportsSchema, "GET", "/api/scheduled-imports", undefined, signal);
export const rerunScheduledImport = (period: string) =>
  request(scheduledJobSchema, "POST", `/api/scheduled-imports/${period}/run`);

export const listNotifications = ({ signal }: Options) =>
  request(z.object({ notifications: z.array(notificationSchema) }), "GET", "/api/notifications", undefined, signal)
    .then((body) => body.notifications);
export const dismissNotification = (id: number) => request(z.undefined(), "POST", `/api/notifications/${id}/dismiss`);
export const resendNotification = (id: number) => request(notificationSchema, "POST", `/api/notifications/${id}/resend`);

export const getInvesting = (range: InvestingRange, { signal }: Options) =>
  request(investingSchema, "GET", `/api/investing?range=${range}`, undefined, signal);
export const updateInvestingSettings = (input: { startedOn: string | null }) =>
  request(z.object({ startedOn: z.string().nullable() }), "PUT", "/api/investing/settings", input);
export const getInvestmentIncome = (year: number | null, { signal }: Options) =>
  request(incomeSchema, "GET", year ? `/api/investing/income?year=${year}` : "/api/investing/income", undefined, signal);
export type InvestingGroupInput = { name: string; pieIds: number[] };
export const createInvestingGroup = (input: InvestingGroupInput) =>
  request(investingGroupSchema, "POST", "/api/investing/groups", input);
export const updateInvestingGroup = (id: number, input: InvestingGroupInput) =>
  request(investingGroupSchema, "PUT", `/api/investing/groups/${id}`, input);
export const deleteInvestingGroup = (id: number) => request(z.undefined(), "DELETE", `/api/investing/groups/${id}`);

// safeNext keeps post-sign-in redirects on this origin and inside the app.
export function safeNext(value: string | null): string {
  if (!value || !value.startsWith("/") || value.startsWith("//") || value.includes("\\")) return "/";
  try {
    const url = new URL(value, window.location.origin);
    if (url.origin !== window.location.origin || url.pathname.startsWith("/login") || url.pathname.startsWith("/onboarding")) return "/";
    return `${url.pathname}${url.search}`;
  } catch {
    return "/";
  }
}

// Queries that read the ledger. Anything that writes transactions reloads
// them all, so totals, balances and the dashboard never disagree.
export const ledgerQueries = ["transactions", "transaction", "statistics", "dashboard", "accounts", "categories", "payees"] as const;

export const listChats = ({ signal }: Options) => request(chatListSchema, "GET", "/api/chats", undefined, signal);
export const getChat = (id: number, { signal }: Options) => request(chatDetailSchema, "GET", `/api/chats/${id}`, undefined, signal);
// createChat starts a chat with its first question.
export const createChat = (question: string) => request(chatReplySchema, "POST", "/api/chats", { question });
export const askInChat = (id: number, question: string) => request(chatReplySchema, "POST", `/api/chats/${id}/messages`, { question });
export const renameChat = (id: number, title: string) => request(chatSchema, "PATCH", `/api/chats/${id}`, { title });
export const deleteChat = (id: number) => request(z.undefined(), "DELETE", `/api/chats/${id}`);
