'use client';

import { useState } from 'react';
import { ApiError, api, type Setting, type SettingsResult } from '@/lib/api';
import { Field, baseline } from './field';

/**
 * One group of settings, with its own Save.
 *
 * **One Save per section is the transaction boundary showing through.** A
 * settings write is all-or-nothing on the Go side, so a form that saved eight
 * sections at once would have to explain which of them was rejected and why the
 * other seven did not land either. A section is small enough that "nothing was
 * written" is a sentence about the thing you are looking at.
 *
 * Only what changed is sent. Absent leaves a setting alone, so an untouched
 * field is never in the body — which is also what keeps a screen with a
 * shadowed field from writing one, and what makes an empty box on a secret
 * mean nothing at all.
 */
export function Section({
  title,
  blurb,
  warning,
  extra,
  settings,
  onSaved,
}: {
  title: string;
  blurb?: string;
  warning?: React.ReactNode;
  /**
   * Something to draw under the fields that is not a field.
   *
   * A function of the section's **effective** values — what the controls are
   * showing right now, edits included — rather than a plain node, because the
   * one caller needs exactly that: turning 1337x on has to show minter's
   * command straight away, and the value that says so is the unsaved edit and
   * not anything the server has been told about yet.
   *
   * It sits below the fields and above Save on purpose. It is not part of the
   * transaction and never contributes to `changes`; it is the answer to "and is
   * the thing this setting needs actually there?", which belongs beside the
   * setting rather than in the table at the bottom of the page.
   */
  extra?: (values: Record<string, string>) => React.ReactNode;
  settings: Setting[];
  onSaved: (next: SettingsResult) => void;
}) {
  // Only what has been touched. undefined is untouched; '' is a deliberate
  // clear. Nothing here is seeded from the response, so a re-render after
  // somebody else's save cannot overwrite what is being typed.
  const [edits, setEdits] = useState<Record<string, string>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);

  // The confirm box is not a setting and is never sent. A section holds at most
  // one password, so one piece of state covers it.
  const [confirm, setConfirm] = useState('');
  const password = settings.find((setting) => setting.kind === 'password' && setting.editable);
  const typedPassword = password ? edits[password.key] : undefined;
  const mismatch = typedPassword !== undefined && typedPassword !== '' && confirm !== typedPassword;

  const changes = Object.fromEntries(
    settings
      .filter((setting) => edits[setting.key] !== undefined)
      .map((setting) => [setting.key, edits[setting.key]]),
  );
  const changed = Object.keys(changes).length;

  function edit(key: string, value: string | undefined) {
    setSaved(false);
    // Both messages describe a submission that no longer matches the form, and
    // they have to clear together: watching the field error disappear while
    // "nothing was saved" stays put reads as a second, unexplained failure.
    setFailure(null);
    setEdits((current) => {
      const next = { ...current };
      if (value === undefined) delete next[key];
      else next[key] = value;
      return next;
    });
    // The message described the value that was rejected, and the value has just
    // changed. Leaving it under a field somebody has already fixed is how a
    // form ends up arguing with itself.
    setErrors((current) => {
      if (current[key] === undefined) return current;
      const next = { ...current };
      delete next[key];
      return next;
    });
  }

  async function save(event: React.FormEvent) {
    event.preventDefault();
    if (!changed || mismatch) return;

    setSaving(true);
    setErrors({});
    setFailure(null);
    try {
      const next = await api.updateSettings(changes);
      setEdits({});
      setConfirm('');
      setSaved(true);
      onSaved(next);
    } catch (cause) {
      // Nothing typed is thrown away. The write was refused as a whole, so the
      // form still holds exactly what was submitted, with the server's message
      // under the field that caused it — a 400 from the parser, or a 409 from
      // a variable that owns the key, which is the same fact arriving late.
      if (cause instanceof ApiError) {
        setErrors(cause.fields);
        setFailure(cause.message);
      } else {
        setFailure(cause instanceof Error ? cause.message : String(cause));
      }
    } finally {
      setSaving(false);
    }
  }

  // Only the password applies without a restart, and a section is uniform in
  // practice — but this reads the flag rather than the group name, so a phase 8
  // setting lands in the right sentence on its own.
  const immediate = settings.every((setting) => !setting.restart_required);

  return (
    <form className="panel section" onSubmit={save}>
      <div className="section-head">
        <h2>{title}</h2>
        {blurb && <p className="small muted">{blurb}</p>}
      </div>

      {warning}

      <div className="section-fields">
        {settings.map((setting) => (
          <Field
            key={setting.key}
            setting={setting}
            edit={edits[setting.key]}
            error={errors[setting.key]}
            confirm={password?.key === setting.key ? confirm : undefined}
            onConfirm={setConfirm}
            mismatch={password?.key === setting.key ? mismatch : undefined}
            onChange={(value) => edit(setting.key, value)}
          />
        ))}
      </div>

      {extra?.(
        Object.fromEntries(
          settings.map((setting) => [setting.key, edits[setting.key] ?? baseline(setting)]),
        ),
      )}

      {failure && (
        <div className="banner error section-failure" role="alert">
          <strong>Nothing in this section was saved</strong>
          <span>{failure}</span>
        </div>
      )}

      <div className="section-save">
        <button className="primary" disabled={saving || !changed || mismatch}>
          {saving ? 'Saving…' : 'Save'}
        </button>
        <span className="small muted">
          {changed === 0
            ? immediate
              ? 'Changes here apply at once.'
              : 'Changes here apply the next time curator starts.'
            : `${changed} change${changed === 1 ? '' : 's'} to save${
                immediate ? ', applying at once' : ', applying at the next start'
              }.`}
        </span>
        {saved && <span className="small">Saved.</span>}
      </div>
    </form>
  );
}
