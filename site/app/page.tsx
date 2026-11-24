const settings: Array<[string, string]> = [
  ["HOME_API_URL", "Required. The public address of your home hub, e.g. https://home.example.com"],
  ["HOME_API_KEY", "Required, secret. Your own key for the hub's REST endpoints (created for you in the hub's admin)."],
  ["HOME_LANGUAGE", "Language to speak with you, e.g. ru. Unset: the language you use."],
];

export default function Home() {
  return (
    <main className="mx-auto max-w-2xl px-6 py-20 text-zinc-900">
      <p className="text-sm uppercase tracking-widest text-zinc-500">Private MCP server</p>
      <h1 className="mt-4 text-4xl font-semibold">Homie</h1>
      <p className="mt-6">
        Ask ChatGPT, by voice or text, to control the devices at home: the TV and whatever else your home hub has
        plugins for. The tools come from the hub; this site only carries them to ChatGPT.
      </p>
      <h2 className="mt-12 text-lg font-semibold">Settings → Environment variables</h2>
      <dl className="mt-4 space-y-3">
        {settings.map(([name, text]) => (
          <div key={name}>
            <dt className="font-mono text-sm">{name}</dt>
            <dd className="text-zinc-600">{text}</dd>
          </div>
        ))}
      </dl>
      <p className="mt-10 text-sm text-zinc-500">Variables apply after the next deploy.</p>
    </main>
  );
}
