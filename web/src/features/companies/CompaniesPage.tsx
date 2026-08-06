import { FormEvent, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useSearchParams } from "react-router-dom";
import { api, type Company } from "../../shared/api/client";
import { useI18n } from "../../shared/i18n/I18nProvider";
import { Button } from "../../shared/ui/Button";
import styles from "./CompaniesPage.module.scss";

export function CompaniesPage() {
  const { locale, t } = useI18n();
  const [name, setName] = useState("");
  const [pendingArchive, setPendingArchive] = useState<Company | null>(null);
  const [pendingDelete, setPendingDelete] = useState<Company | null>(null);
  const [vkCompany, setVkCompany] = useState<Company | null>(null);
  const [pendingVKDisconnect, setPendingVKDisconnect] = useState<{
    platformAccountId: string;
    displayName: string;
  } | null>(null);
  const [vkDisconnectSuccess, setVKDisconnectSuccess] = useState(false);
  const [syncQueued, setSyncQueued] = useState(false);
  const [vkForm, setVkForm] = useState<{
    accessMethod: "LOGIN" | "PHONE";
    login: string;
    password: string;
    phone: string;
  }>({ accessMethod: "LOGIN", login: "", password: "", phone: "" });
  const [params] = useSearchParams();
  const queryClient = useQueryClient();
  const me = useQuery({ queryKey: ["me"], queryFn: api.me });
  const companies = useQuery({
    queryKey: ["companies", locale],
    queryFn: api.companies,
  });
  const vkAccounts = useQuery({
    queryKey: ["company-vk-accounts", locale],
    queryFn: api.companyVkAccounts,
    refetchInterval: vkCompany ? 10_000 : false,
  });
  const create = useMutation({
    mutationFn: () => api.createCompany(name),
    onSuccess: async () => {
      setName("");
      await queryClient.invalidateQueries({ queryKey: ["companies"] });
    },
  });
  const archive = useMutation({
    mutationFn: api.archiveCompany,
    onSuccess: async () => {
      setPendingArchive(null);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["companies"] }),
        queryClient.invalidateQueries({ queryKey: ["creators"] }),
      ]);
    },
  });
  const remove = useMutation({
    mutationFn: api.deleteCompany,
    onSuccess: async () => {
      setPendingDelete(null);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["companies"] }),
        queryClient.invalidateQueries({ queryKey: ["creators"] }),
        queryClient.invalidateQueries({ queryKey: ["company-vk-accounts"] }),
      ]);
    },
  });
  const saveVK = useMutation({
    mutationFn: () => api.saveCompanyVkAccount(vkCompany?.id ?? "", vkForm),
    onSuccess: async () => {
      setVkCompany(null);
      setVkForm({ accessMethod: "LOGIN", login: "", password: "", phone: "" });
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["companies"] }),
        queryClient.invalidateQueries({ queryKey: ["company-vk-accounts"] }),
      ]);
    },
  });
  const revealVK = useMutation({
    mutationFn: (accountId: string) => api.revealCompanyVkPassword(accountId),
    onSuccess: ({ value }) =>
      setVkForm((current) => ({ ...current, password: value })),
  });
  const authorizeVK = useMutation({
    mutationFn: (companyId: string) =>
      api.startCompanyVkAuthorization(companyId),
    onSuccess: ({ authorizationUrl }) =>
      window.location.assign(authorizationUrl),
  });
  const syncVK = useMutation({
    mutationFn: (platformAccountId: string) =>
      api.requestPlatformSync(platformAccountId),
    onSuccess: async () => {
      setSyncQueued(true);
      await queryClient.invalidateQueries({
        queryKey: ["company-vk-accounts"],
      });
    },
  });
  const disconnectVK = useMutation({
    mutationFn: (platformAccountId: string) =>
      api.disconnectPlatformAccount(platformAccountId),
    onMutate: () => setVKDisconnectSuccess(false),
    onSuccess: async () => {
      setPendingVKDisconnect(null);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["company-vk-accounts"] }),
        queryClient.invalidateQueries({ queryKey: ["companies"] }),
        queryClient.invalidateQueries({ queryKey: ["integrations"] }),
        queryClient.invalidateQueries({ queryKey: ["platform-connections"] }),
        queryClient.invalidateQueries({ queryKey: ["creator-vk-access"] }),
        queryClient.invalidateQueries({ queryKey: ["creators"] }),
        queryClient.invalidateQueries({ queryKey: ["summary"] }),
      ]);
      setVKDisconnectSuccess(true);
    },
  });
  const canDisconnect = me.data?.role === "ADMIN" || me.data?.role === "ANALYST";
  function openVK(company: Company) {
    const account = vkAccounts.data?.items.find(
      (item) => item.companyId === company.id,
    );
    setVkForm({
      accessMethod: account?.accessMethod ?? "LOGIN",
      login: account?.login ?? "",
      password: "",
      phone: account?.phone ?? "",
    });
    setSyncQueued(false);
    setVKDisconnectSuccess(false);
    disconnectVK.reset();
    setVkCompany(company);
  }
  function submit(event: FormEvent) {
    event.preventDefault();
    if (name.trim()) create.mutate();
  }

  return (
    <section className={styles.page}>
      <header>
        <div>
          <p>{t('СТРУКТУРА КОМАНДЫ')}</p>
          <h1>{t('Компании')}</h1>
          <span>{t('Группируйте креаторов по брендам и направлениям, для которых они создают контент.')}</span>
        </div>
      </header>
      <form className={styles.create} onSubmit={submit}>
        <label>
          <span>{t('Новая компания')}</span>
          <input
            value={name}
            onChange={(event) => setName(event.target.value)}
            placeholder={t('Например, Поле чудес')}
          />
        </label>
        <Button type="submit" disabled={!name.trim() || create.isPending}>
          {create.isPending ? t('Создаём…') : t('Создать компанию')}
        </Button>
      </form>
      {create.isError ? (
        <p className={styles.error}>{t(create.error.message)}</p>
      ) : null}
      {params.get("oauth") === "connected" ? (
        <p className={styles.success}>
          {t('Общий аккаунт VK ID подключён. Сбор сообществ поставлен в очередь.')}
        </p>
      ) : params.get("oauth") ? (
        <p className={styles.error}>
          {t('Подключение VK не завершено:')} {params.get("oauth")}.
        </p>
      ) : null}
      {companies.isPending ? (
        <div className={styles.state}>{t('Загружаем компании…')}</div>
      ) : companies.isError ? (
        <div className={styles.error}>{t(companies.error.message)}</div>
      ) : companies.data.items.length ? (
        <div className={styles.grid}>
          {companies.data.items.map((company) => {
            const vkAccount = vkAccounts.data?.items.find(
              (item) => item.companyId === company.id,
            );
            const vkIDConnected = vkAccount?.oauthStatus === "ACTIVE";
            return (
              <article key={company.id}>
                <div className={styles.mark}>
                  {company.name.slice(0, 1).toUpperCase()}
                </div>
                <div className={styles.companyInfo}>
                  <h2>{company.name}</h2>
                  <p>
                    {company.creatorCount
                      ? `${company.creatorCount} ${company.creatorCount === 1 ? t('креатор') : t('креаторов')}`
                      : t('Креаторы ещё не назначены')}
                  </p>
                </div>
                <div className={styles.actions}>
                  <button
                    type="button"
                    className={vkIDConnected ? styles.vkReady : styles.vkSetup}
                    onClick={() => openVK(company)}
                  >
                    {vkIDConnected
                      ? t('VK ID подключён')
                      : company.hasVkAccount
                        ? t('Подключить VK ID')
                        : t('Настроить VK')}
                  </button>
                  <button
                    type="button"
                    className={styles.archive}
                    onClick={() => setPendingArchive(company)}
                  >
                    {t('Архивировать')}
                  </button>
                  <button
                    type="button"
                    className={styles.delete}
                    onClick={() => setPendingDelete(company)}
                  >
                    {t('Удалить')}
                  </button>
                </div>
              </article>
            );
          })}
        </div>
      ) : (
        <div className={styles.state}>
          {t('Компаний пока нет. Создайте первую выше.')}
        </div>
      )}
      {pendingArchive ? (
        <div
          className={styles.backdrop}
          role="presentation"
          onMouseDown={(event) => {
            if (event.target === event.currentTarget && !archive.isPending)
              setPendingArchive(null);
          }}
        >
          <div className={styles.dialog} role="dialog" aria-modal="true">
            <h2>{t('Архивировать')} «{pendingArchive.name}»?</h2>
            <p>{t('Компания исчезнет из списка, а её креаторы перейдут в категорию «Без компании». Карточки и статистика сохранятся.')}</p>
            {archive.isError ? (
              <p className={styles.error}>{t(archive.error.message)}</p>
            ) : null}
            <footer>
              <button
                onClick={() => setPendingArchive(null)}
                disabled={archive.isPending}
              >
                {t('Отмена')}
              </button>
              <button
                className={styles.confirm}
                onClick={() => archive.mutate(pendingArchive.id)}
                disabled={archive.isPending}
              >
                {archive.isPending ? t('Архивируем…') : t('Архивировать')}
              </button>
            </footer>
          </div>
        </div>
      ) : null}
      {pendingDelete ? (
        <div
          className={styles.backdrop}
          role="presentation"
          onMouseDown={(event) => {
            if (event.target === event.currentTarget && !remove.isPending)
              setPendingDelete(null);
          }}
        >
          <div className={styles.dialog} role="dialog" aria-modal="true">
            <h2>{t('Удалить')} «{pendingDelete.name}»?</h2>
            <p>{t('Компания и её настройки VK будут удалены навсегда. Креаторы перейдут в категорию «Без компании», а их карточки и статистика сохранятся.')}</p>
            {remove.isError ? (
              <p className={styles.error}>{t(remove.error.message)}</p>
            ) : null}
            <footer>
              <button
                onClick={() => setPendingDelete(null)}
                disabled={remove.isPending}
              >
                {t('Отмена')}
              </button>
              <button
                className={styles.confirm}
                onClick={() => remove.mutate(pendingDelete.id)}
                disabled={remove.isPending}
              >
                {remove.isPending ? t('Удаляем…') : t('Удалить навсегда')}
              </button>
            </footer>
          </div>
        </div>
      ) : null}
      {vkCompany ? (
        <div
          className={styles.backdrop}
          role="presentation"
          onMouseDown={(event) => {
            if (event.target === event.currentTarget && !saveVK.isPending)
              setVkCompany(null);
          }}
        >
          <form
            className={`${styles.dialog} ${styles.vkDialog}`}
            role="dialog"
            aria-modal="true"
            aria-labelledby="company-vk-title"
            onSubmit={(event) => {
              event.preventDefault();
              saveVK.mutate();
            }}
          >
            <div className={styles.dialogHead}>
              <div>
                <span>{t('ОБЩИЙ ДОСТУП КОМПАНИИ')}</span>
                <h2 id="company-vk-title">VK · {vkCompany.name}</h2>
              </div>
              <button
                type="button"
                aria-label={t('Закрыть')}
                onClick={() => setVkCompany(null)}
              >
                ×
              </button>
            </div>
            <p>{t('Этот аккаунт управляет сообществами креаторов. Подключите его один раз через VK ID — статистика будет собираться из сообществ, указанных в карточках креаторов.')}</p>
            {vkAccounts.isError ? (
              <p className={styles.error}>{t(vkAccounts.error.message)}</p>
            ) : null}
            {(() => {
              const account = vkAccounts.data?.items.find(
                (item) => item.companyId === vkCompany.id,
              ) ?? {
                oauthStatus: "",
                oauthDisplayName: "",
                oauthUsername: "",
                oauthAvatarUrl: "",
                oauthProfileUrl: "",
                communityCount: 0,
                syncError: "",
                lastSuccessAt: null,
                platformAccountId: "",
              };
              return (
                <div className={styles.oauthBox}>
                  {account.oauthStatus === "ACTIVE" ? (
                    <div className={styles.connectedProfile}>
                      {account.oauthAvatarUrl ? (
                        <img src={account.oauthAvatarUrl} alt="" />
                      ) : (
                        <span className={styles.profilePlaceholder}>VK</span>
                      )}
                      <div>
                        <b>{t('VK ID подключён')}</b>
                        {account.oauthProfileUrl ? (
                          <a href={account.oauthProfileUrl} target="_blank" rel="noreferrer">
                            {account.oauthDisplayName || account.oauthUsername} ↗
                          </a>
                        ) : (
                          <span>{account.oauthDisplayName || account.oauthUsername}</span>
                        )}
                      </div>
                    </div>
                  ) : (
                    <b>{t('VK ID ещё не подключён')}</b>
                  )}
                  <span>{t('Сообществ назначено:')} {account.communityCount}</span>
                  {account.syncError ? (
                    <span className={styles.syncError}>
                      {t('Ошибка синхронизации:')} {t(account.syncError)}
                    </span>
                  ) : account.lastSuccessAt ? (
                    <span className={styles.syncSuccess}>
                      {t('Последняя успешная синхронизация:')}{" "}
                      {new Date(account.lastSuccessAt).toLocaleString(locale === 'en' ? 'en-US' : 'ru-RU')}
                    </span>
                  ) : account.oauthStatus === "ACTIVE" ? (
                    <span>{t('Первая синхронизация ещё не завершена.')}</span>
                  ) : null}
                  <div className={styles.oauthActions}>
                    <button
                      type="button"
                      onClick={() => authorizeVK.mutate(vkCompany.id)}
                      disabled={authorizeVK.isPending}
                    >
                      {authorizeVK.isPending
                        ? t('Переходим…')
                        : account.oauthStatus === "ACTIVE"
                          ? t('Переподключить VK ID')
                          : t('Подключить VK ID')}
                    </button>
                    {account.oauthStatus === "ACTIVE" &&
                    account.platformAccountId ? (
                      <>
                        <button
                          type="button"
                          onClick={() => syncVK.mutate(account.platformAccountId)}
                          disabled={syncVK.isPending || disconnectVK.isPending}
                        >
                          {syncVK.isPending
                            ? t('Ставим в очередь…')
                            : syncQueued
                              ? t('Синхронизация запрошена')
                              : t('Синхронизировать сейчас')}
                        </button>
                        {canDisconnect ? (
                          <button
                            type="button"
                            className={styles.disconnectVK}
                            onClick={() => {
                              disconnectVK.reset();
                              setPendingVKDisconnect({
                                platformAccountId: account.platformAccountId,
                                displayName: account.oauthDisplayName || account.oauthUsername || "VK ID",
                              });
                            }}
                            disabled={disconnectVK.isPending}
                          >
                            {t('Отвязать VK ID')}
                          </button>
                        ) : null}
                      </>
                    ) : null}
                  </div>
                  {vkDisconnectSuccess ? (
                    <span className={styles.syncSuccess}>{t('VK ID отвязан. Собранные данные сохранены.')}</span>
                  ) : null}
                  {authorizeVK.isError ? (
                    <span className={styles.error}>
                      {t(authorizeVK.error.message)}
                    </span>
                  ) : null}
                  {syncVK.isError ? (
                    <span className={styles.error}>{t(syncVK.error.message)}</span>
                  ) : null}
                </div>
              );
            })()}
            <fieldset className={styles.accessMethod}>
              <legend>{t('Данные общего доступа для команды')}</legend>
              <label>
                <input
                  type="radio"
                  name="vk-access-method"
                  checked={vkForm.accessMethod === "LOGIN"}
                  onChange={() =>
                    setVkForm((current) => ({
                      ...current,
                      accessMethod: "LOGIN",
                    }))
                  }
                />
                {t('Логин и пароль')}
              </label>
              <label>
                <input
                  type="radio"
                  name="vk-access-method"
                  checked={vkForm.accessMethod === "PHONE"}
                  onChange={() =>
                    setVkForm((current) => ({
                      ...current,
                      accessMethod: "PHONE",
                      login: "",
                      password: "",
                    }))
                  }
                />
                {t('Только номер телефона')}
              </label>
            </fieldset>
            <div className={styles.vkFields}>
              {vkForm.accessMethod === "LOGIN" ? (
                <>
                  <label>
                    {t('Логин')}
                    <input
                      required
                      value={vkForm.login}
                      onChange={(event) =>
                        setVkForm((current) => ({
                          ...current,
                          login: event.target.value,
                        }))
                      }
                      autoComplete="off"
                    />
                  </label>
                  <label>
                    {t('Пароль')}
                    <div className={styles.passwordField}>
                      <input
                        required={
                          !vkCompany.hasVkAccount ||
                          (vkCompany.hasVkAccount &&
                            vkAccounts.data?.items.find(
                              (item) => item.companyId === vkCompany.id,
                            )?.accessMethod !== "LOGIN")
                        }
                        type="password"
                        value={vkForm.password}
                        onChange={(event) =>
                          setVkForm((current) => ({
                            ...current,
                            password: event.target.value,
                          }))
                        }
                        autoComplete="new-password"
                        placeholder={
                          vkCompany.hasVkAccount
                            ? t('Сохранён — введите только для замены')
                            : t('Введите пароль')
                        }
                      />
                      {vkCompany.hasVkAccount &&
                      !vkForm.password &&
                      vkAccounts.data?.items.find(
                        (item) => item.companyId === vkCompany.id,
                      )?.hasPassword ? (
                        <button
                          type="button"
                          onClick={() => {
                            const account = vkAccounts.data?.items.find(
                              (item) => item.companyId === vkCompany.id,
                            );
                            if (account) revealVK.mutate(account.id);
                          }}
                          disabled={revealVK.isPending}
                        >
                          {revealVK.isPending ? t('Открываем…') : t('Показать')}
                        </button>
                      ) : null}
                    </div>
                  </label>
                  <label>
                    {t('Телефон')}
                    <input
                      value={vkForm.phone}
                      onChange={(event) =>
                        setVkForm((current) => ({
                          ...current,
                          phone: event.target.value,
                        }))
                      }
                      autoComplete="off"
                      placeholder={t('Необязательно')}
                    />
                  </label>
                </>
              ) : (
                <label>
                  {t('Номер телефона')}
                  <input
                    required
                    type="tel"
                    value={vkForm.phone}
                    onChange={(event) =>
                      setVkForm((current) => ({
                        ...current,
                        phone: event.target.value,
                      }))
                    }
                    autoComplete="tel"
                    placeholder="+7 999 000-00-00"
                  />
                </label>
              )}
            </div>
            {saveVK.isError ? (
              <p className={styles.error}>{t(saveVK.error.message)}</p>
            ) : null}
            {revealVK.isError ? (
              <p className={styles.error}>{t(revealVK.error.message)}</p>
            ) : null}
            <footer>
              <button
                type="button"
                onClick={() => setVkCompany(null)}
                disabled={saveVK.isPending}
              >
                {t('Отмена')}
              </button>
              <button
                type="submit"
                className={styles.saveVK}
                disabled={
                  saveVK.isPending ||
                  (vkForm.accessMethod === "LOGIN"
                    ? !vkForm.login.trim()
                    : !vkForm.phone.trim())
                }
              >
                {saveVK.isPending ? t('Сохраняем…') : t('Сохранить VK')}
              </button>
            </footer>
          </form>
        </div>
      ) : null}
      {pendingVKDisconnect ? (
        <div
          className={styles.backdrop}
          role="presentation"
          onMouseDown={(event) => {
            if (event.target === event.currentTarget && !disconnectVK.isPending) {
              setPendingVKDisconnect(null);
              disconnectVK.reset();
            }
          }}
        >
          <div className={styles.dialog} role="dialog" aria-modal="true" aria-labelledby="disconnect-vk-title">
            <h2 id="disconnect-vk-title">{t('Отвязать VK ID?')}</h2>
            <p><b>{pendingVKDisconnect.displayName}</b></p>
            <p>{t('Доступ VK будет отозван, а новые синхронизации сообществ остановятся. Уже собранные публикации и метрики сохранятся.')}</p>
            {disconnectVK.isError ? (
              <p className={styles.error}>{t(disconnectVK.error.message)}</p>
            ) : null}
            <footer>
              <button
                type="button"
                onClick={() => {
                  setPendingVKDisconnect(null);
                  disconnectVK.reset();
                }}
                disabled={disconnectVK.isPending}
              >
                {t('Отмена')}
              </button>
              <button
                type="button"
                className={styles.confirm}
                onClick={() => disconnectVK.mutate(pendingVKDisconnect.platformAccountId)}
                disabled={disconnectVK.isPending}
              >
                {disconnectVK.isPending ? t('Отвязываем…') : t('Отвязать')}
              </button>
            </footer>
          </div>
        </div>
      ) : null}
    </section>
  );
}
