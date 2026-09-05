import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, safePublicationPermalink, type ContentItemSummary, type ContentTarget } from "../../shared/api/client";
import { Panel, StatusBadge } from "../../shared/ui/Primitives";
import { useI18n } from "../../shared/i18n/I18nProvider";
import {
  publishingErrorMessage,
  useContentAttempts,
  useContentItem,
  usePublishingItems,
} from "./hooks/publishing";
import styles from "./PublishingPage.module.scss";

const moscowDay = (value: string) =>
  new Intl.DateTimeFormat("en-CA", {
    timeZone: "Europe/Moscow",
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).format(new Date(value));
const moscowTime = (value: string, locale: string) =>
  new Intl.DateTimeFormat(locale === "en" ? "en-US" : "ru-RU", {
    timeZone: "Europe/Moscow",
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(value));
export const monthRange = (month: Date) => {
  const from = new Date(
    Date.UTC(month.getUTCFullYear(), month.getUTCMonth(), 1),
  );
  const until = new Date(
    Date.UTC(month.getUTCFullYear(), month.getUTCMonth() + 1, 1),
  );
  return { from: from.toISOString(), until: until.toISOString() };
};

export function PublicationIdentity({
  target,
  t,
}: {
  target: Pick<ContentTarget, "platform" | "status" | "externalId" | "externalUrl">;
  t: (key: string) => string;
}) {
  if (target.status !== "SUCCEEDED") return null;
  const href = safePublicationPermalink(target.platform, target.externalUrl);
  if (href) {
    return (
      <a href={href} target="_blank" rel="noopener noreferrer">
        {t("Открыть публикацию")}
      </a>
    );
  }
  return target.externalId ? <small>ID: {target.externalId}</small> : null;
}

export default function PlanningBoard({ own }: { own: boolean }) {
  const { t, locale } = useI18n();
  const [view, setView] = useState<"list" | "calendar">("list");
  const [month, setMonth] = useState(() => new Date());
  const [selected, setSelected] = useState("");
  const items = usePublishingItems(own);
  const calendar = usePublishingItems(own, monthRange(month));
  const activeQuery = view === "calendar" ? calendar : items;
  const content = activeQuery.data?.items ?? [];
  return (
    <>
      <Panel className={styles.planning}>
        <header>
          <div>
            <h2>{t("Черновики и расписание")}</h2>
            <small>{t("Время указано по Москве")}</small>
          </div>
          <div className={styles.viewToggle}>
            <button
              aria-pressed={view === "list"}
              onClick={() => setView("list")}
            >
              {t("Список")}
            </button>
            <button
              aria-pressed={view === "calendar"}
              onClick={() => setView("calendar")}
            >
              {t("Календарь")}
            </button>
            <a href="/app/publications">{t("Опубликованные")}</a>
          </div>
        </header>
        {activeQuery.isPending ? (
          <p className={styles.loading} role="status">
            {t("Загрузка…")}
          </p>
        ) : activeQuery.isError ? (
          <p className={styles.loadError} role="alert">
            {publishingErrorMessage(activeQuery.error, t)}
          </p>
        ) : view === "calendar" ? (
          <Calendar
            month={month}
            items={content}
            locale={locale}
            t={t}
            onMonth={setMonth}
            onSelect={setSelected}
          />
        ) : (
          <div className={styles.planningList}>
            {content.map((item) => (
              <ContentCard
                key={item.id}
                item={item}
                locale={locale}
                t={t}
                onClick={() => setSelected(item.id)}
              />
            ))}
            {!content.length ? (
              <p className={styles.empty}>
                {t("Запланированных публикаций пока нет.")}
              </p>
            ) : null}
          </div>
        )}
      </Panel>
      {selected ? (
        <Detail
          id={selected}
          own={own}
          locale={locale}
          t={t}
          close={() => setSelected("")}
        />
      ) : null}
    </>
  );
}

function Calendar({
  month,
  items,
  locale,
  t,
  onMonth,
  onSelect,
}: {
  month: Date;
  items: ContentItemSummary[];
  locale: string;
  t: (v: string) => string;
  onMonth: (v: Date) => void;
  onSelect: (id: string) => void;
}) {
  const range = monthRange(month);
  const first = new Date(range.from);
  const start = new Date(first);
  start.setUTCDate(1 - ((first.getUTCDay() + 6) % 7));
  const days = Array.from(
    { length: 42 },
    (_, i) => new Date(start.getTime() + i * 86400000),
  );
  // Avoid UTC/local date conversion in labels; Moscow has a fixed UTC+3 offset.
  const dayItems = (key: string) =>
    items.filter(
      (item) => moscowDay(item.scheduledAt ?? item.updatedAt) === key,
    );
  return (
    <div className={styles.calendar}>
      <div className={styles.monthNav}>
        <button
          onClick={() =>
            onMonth(
              new Date(
                Date.UTC(month.getUTCFullYear(), month.getUTCMonth() - 1, 1),
              ),
            )
          }
        >
          ‹
        </button>
        <b>
          {new Intl.DateTimeFormat(locale === "en" ? "en-US" : "ru-RU", {
            timeZone: "Europe/Moscow",
            month: "long",
            year: "numeric",
          }).format(month)}
        </b>
        <button
          onClick={() =>
            onMonth(
              new Date(
                Date.UTC(month.getUTCFullYear(), month.getUTCMonth() + 1, 1),
              ),
            )
          }
        >
          ›
        </button>
      </div>
      <div className={styles.calendarGrid}>
        {days.map((day) => {
          const key = day.toISOString().slice(0, 10);
          const entries = dayItems(key);
          return (
            <div
              key={key}
              className={
                day.getUTCMonth() === month.getUTCMonth()
                  ? styles.calendarDay
                  : styles.calendarMuted
              }
            >
              <b>{day.getUTCDate()}</b>
              {entries.slice(0, 3).map((item) => (
                <button key={item.id} onClick={() => onSelect(item.id)}>
                  {item.creatorName || t("Публикация")} ·{" "}
                  {item.platforms?.join(", ")}
                </button>
              ))}
            </div>
          );
        })}
      </div>
      <div className={styles.agenda}>
        {items.map((item) => (
          <ContentCard
            key={item.id}
            item={item}
            locale={locale}
            t={t}
            onClick={() => onSelect(item.id)}
          />
        ))}
      </div>
    </div>
  );
}
function ContentCard({
  item,
  locale,
  t,
  onClick,
}: {
  item: ContentItemSummary;
  locale: string;
  t: (v: string) => string;
  onClick: () => void;
}) {
  const at = item.scheduledAt ?? item.updatedAt;
  return (
    <button className={styles.contentCard} onClick={onClick}>
      <span>
        <b>
          {item.creatorName || t("Публикация")} #{item.id.slice(0, 8)}
        </b>
        <small>
          {moscowTime(at, locale)} ·{" "}
          {item.platforms?.join(", ") || t("Площадки")}
        </small>
      </span>
      <span>
        {item.attentionCount ? (
          <StatusBadge tone="warning">
            {t("Нужны работы")}: {item.attentionCount}
          </StatusBadge>
        ) : (
          <StatusBadge>
            {t("Ревизия")} {item.revision}
          </StatusBadge>
        )}
        <small>
          {item.targetCount ?? 0} {t("целей")}
        </small>
      </span>
    </button>
  );
}
function Detail({
  id,
  own,
  locale,
  t,
  close,
}: {
  id: string;
  own: boolean;
  locale: string;
  t: (v: string) => string;
  close: () => void;
}) {
  const detail = useContentItem(id, own);
  const [history, setHistory] = useState(false);
  const [retryIDs, setRetryIDs] = useState<string[]>([]);
  const attempts = useContentAttempts(id, own, history);
  const client = useQueryClient();
  const retry = useMutation({
    mutationFn: (targetIds: string[]) =>
      own
        ? api.commandOwnContentItem(
            id,
            "retry",
            undefined,
            crypto.randomUUID(),
            targetIds,
          )
        : api.commandContentItem(
            id,
            "retry",
            undefined,
            crypto.randomUUID(),
            targetIds,
          ),
    onSuccess: () => client.invalidateQueries({ queryKey: ["publishing"] }),
  });
  const copy = useMutation({
    mutationFn: () => api.copyContentItem(id, crypto.randomUUID(), own),
    onSuccess: () => client.invalidateQueries({ queryKey: ["publishing"] }),
  });
  if (detail.isPending)
    return (
      <div className={styles.dialogBackdrop}>
        <Panel>{t("Загрузка…")}</Panel>
      </div>
    );
  if (detail.isError || !detail.data)
    return (
      <div className={styles.dialogBackdrop}>
        <Panel>
          <p>{publishingErrorMessage(detail.error, t)}</p>
          <button onClick={close}>{t("Закрыть")}</button>
        </Panel>
      </div>
    );
  const failed = detail.data.targets.filter((x) =>
    ["FAILED", "WAITING_FOR_REAUTH", "RETRY_SCHEDULED"].includes(x.status),
  );
  const toggle = (targetID: string) =>
    setRetryIDs((ids) =>
      ids.includes(targetID)
        ? ids.filter((id) => id !== targetID)
        : [...ids, targetID],
    );
  const attemptItems = attempts.data?.pages.flatMap((page) => page.items) ?? [];
  return (
    <div className={styles.dialogBackdrop} role="dialog" aria-modal="true" tabIndex={-1} onKeyDown={event => { if (event.key === 'Escape') { event.preventDefault(); close() } }}>
      <Panel className={styles.detail}>
        <header>
          <div>
            <small>
              {t("Ревизия")} {detail.data.revision} · {detail.data.status}
            </small>
            <h2>
              {t("Публикация")} #{id.slice(0, 8)}
            </h2>
          </div>
          <button onClick={close}>{t("Закрыть")}</button>
        </header>
        <p>{detail.data.description}</p>
        {detail.data.approval?.status ? (
          <p>
            <b>{t("Согласование")}:</b> {detail.data.approval.status} ·{" "}
            {detail.data.approval.requester}
            {detail.data.approval.decider
              ? ` → ${detail.data.approval.decider}`
              : ""}
            {detail.data.approval.note ? ` · ${detail.data.approval.note}` : ""}
          </p>
        ) : null}
        <div className={styles.targetList}>
          {detail.data.targets.map((target) => (
            <article key={target.id}>
              <div>
                {failed.some((x) => x.id === target.id) ? (
                  <label>
                    <input
                      type="checkbox"
                      checked={retryIDs.includes(target.id)}
                      onChange={() => toggle(target.id)}
                    />{" "}
                    {t("Выбрать для повтора")}
                  </label>
                ) : null}
                <b>{target.platform}</b>
                <small>
                  {target.status} ·{" "}
                  {target.scheduledAt
                    ? moscowTime(target.scheduledAt, locale)
                    : ""}
                </small>
                <PublicationIdentity target={target} t={t} />
              </div>
              {target.errorMessage ? (
                <p className={styles.targetError}>
                  {target.errorCode}: {target.errorMessage}
                </p>
              ) : null}
            </article>
          ))}
        </div>
        <div className={styles.detailActions}>
          {failed.length ? (
            <>
              <button onClick={() => setRetryIDs(failed.map((x) => x.id))}>
                {t("Выбрать все ошибки")}
              </button>
              <button
                disabled={retry.isPending || !retryIDs.length}
                onClick={() => retry.mutate(retryIDs)}
              >
                {t("Повторить выбранные ошибки")} ({retryIDs.length})
              </button>
            </>
          ) : null}
          <button disabled={copy.isPending} onClick={() => copy.mutate()}>
            {t("Создать копию")}
          </button>
          <button onClick={() => setHistory((v) => !v)}>
            {history ? t("Скрыть историю") : t("История попыток")}
          </button>
        </div>
        {retry.isError ? (
          <p className={styles.targetError}>
            {publishingErrorMessage(retry.error, t)}
          </p>
        ) : null}
        {history ? (
          <section className={styles.attempts}>
            {attempts.isPending ? (
              <p>{t("Загрузка…")}</p>
            ) : attempts.isError ? (
              <p className={styles.targetError}>
                {publishingErrorMessage(attempts.error, t)}
              </p>
            ) : (
              attemptItems.map((a) => (
                <article key={a.id}>
                  <b>{a.status}</b>
                  <small>
                    {moscowTime(a.startedAt, locale)} · #{a.id}
                  </small>
                  {a.errorMessage ? (
                    <p>
                      {a.errorCode}: {a.errorMessage}
                    </p>
                  ) : null}
                </article>
              ))
            )}
            {attempts.hasNextPage ? (
              <button
                onClick={() => attempts.fetchNextPage()}
                disabled={attempts.isFetchingNextPage}
              >
                {t("Загрузить ещё")}
              </button>
            ) : null}
          </section>
        ) : null}
      </Panel>
    </div>
  );
}
