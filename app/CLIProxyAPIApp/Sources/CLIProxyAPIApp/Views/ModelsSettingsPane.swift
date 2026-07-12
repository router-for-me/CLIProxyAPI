import SwiftUI

struct SubscriptionModel: Codable, Identifiable {
    let id: String
    let displayName: String?
    let ownedBy: String?

    enum CodingKeys: String, CodingKey {
        case id
        case displayName = "display_name"
        case ownedBy = "owned_by"
    }
}

struct Subscription: Codable, Identifiable {
    let file: String
    let provider: String
    let models: [SubscriptionModel]

    var id: String { file }
}

struct ExposedModelsResponse: Codable {
    let models: [String]
}

struct SubscriptionsResponse: Codable {
    let subscriptions: [Subscription]
}

@MainActor
@Observable
final class ModelsStore {
    private(set) var subscriptions: [Subscription] = []
    private(set) var exposedModels: Set<String> = []
    private(set) var isLoading = false
    private(set) var lastError: String?

    func fetch(baseURL: URL, secret: String) async {
        isLoading = true
        lastError = nil
        defer { isLoading = false }

        do {
            let subs = try await fetchSubscriptions(baseURL: baseURL, secret: secret)
            let exposed = try await fetchExposedModels(baseURL: baseURL, secret: secret)
            subscriptions = subs
            exposedModels = Set(exposed)
        } catch {
            lastError = error.localizedDescription
        }
    }

    func extractAuth(baseURL: URL, secret: String) async {
        isLoading = true
        lastError = nil
        defer { isLoading = false }

        do {
            let url = baseURL.appendingPathComponent("v0/management/extract-auth")
            var request = URLRequest(url: url)
            request.setValue(secret, forHTTPHeaderField: "X-Management-Key")
            request.httpMethod = "POST"
            let (_, response) = try await URLSession.shared.data(for: request)
            guard let http = response as? HTTPURLResponse, http.statusCode == 200 else {
                throw URLError(.badServerResponse)
            }
            await fetch(baseURL: baseURL, secret: secret)
        } catch {
            lastError = error.localizedDescription
        }
    }

    func toggle(_ slug: String, baseURL: URL, secret: String) async {
        if exposedModels.contains(slug) {
            exposedModels.remove(slug)
        } else {
            exposedModels.insert(slug)
        }
        await save(baseURL: baseURL, secret: secret)
    }

    private func save(baseURL: URL, secret: String) async {
        do {
            let url = baseURL.appendingPathComponent("v0/management/exposed-models")
            var request = URLRequest(url: url)
            request.setValue(secret, forHTTPHeaderField: "X-Management-Key")
            request.httpMethod = "PUT"
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
            request.httpBody = try JSONEncoder().encode(["models": Array(exposedModels).sorted()])
            let (_, response) = try await URLSession.shared.data(for: request)
            guard let http = response as? HTTPURLResponse, http.statusCode == 200 else {
                throw URLError(.badServerResponse)
            }
        } catch {
            lastError = error.localizedDescription
        }
    }

    private func fetchSubscriptions(baseURL: URL, secret: String) async throws -> [Subscription] {
        let url = baseURL.appendingPathComponent("v0/management/subscriptions")
        var request = URLRequest(url: url)
        request.setValue(secret, forHTTPHeaderField: "X-Management-Key")
        let (data, response) = try await URLSession.shared.data(for: request)
        guard let http = response as? HTTPURLResponse, http.statusCode == 200 else {
            throw URLError(.badServerResponse)
        }
        let decoded = try JSONDecoder().decode(SubscriptionsResponse.self, from: data)
        return decoded.subscriptions
    }

    private func fetchExposedModels(baseURL: URL, secret: String) async throws -> [String] {
        let url = baseURL.appendingPathComponent("v0/management/exposed-models")
        var request = URLRequest(url: url)
        request.setValue(secret, forHTTPHeaderField: "X-Management-Key")
        let (data, response) = try await URLSession.shared.data(for: request)
        guard let http = response as? HTTPURLResponse, http.statusCode == 200 else {
            throw URLError(.badServerResponse)
        }
        let decoded = try JSONDecoder().decode(ExposedModelsResponse.self, from: data)
        return decoded.models
    }
}

struct ModelsSettingsPane: View {
    @Bindable var settings: BridgeSettingsStore
    @Bindable var bridge: BridgeProcessController
    @State private var store = ModelsStore()
    @State private var searchText = ""

    private var filteredSubscriptions: [Subscription] {
        let query = searchText.lowercased()
        if query.isEmpty { return store.subscriptions }
        return store.subscriptions.map { subscription in
            let filteredModels = subscription.models.filter {
                $0.id.lowercased().contains(query) ||
                ($0.displayName ?? "").lowercased().contains(query)
            }
            return Subscription(file: subscription.file, provider: subscription.provider, models: filteredModels)
        }.filter { !$0.models.isEmpty }
    }

    var body: some View {
        ScrollView(.vertical, showsIndicators: true) {
            VStack(alignment: .leading, spacing: 16) {
                SettingsSection(title: "Models", caption: "Select which models are exposed at /v1/models. Codex will see only the selected models.") {
                    HStack(spacing: 10) {
                        Button {
                            Task { await store.extractAuth(baseURL: settings.baseURL, secret: settings.managementSecret) }
                        } label: {
                            Label("Detect", systemImage: "magnifyingglass")
                        }

                        Button {
                            Task { await store.fetch(baseURL: settings.baseURL, secret: settings.managementSecret) }
                        } label: {
                            Label("Refresh", systemImage: "arrow.clockwise")
                        }

                        if store.isLoading {
                            ProgressView()
                                .controlSize(.small)
                        }

                        if let err = store.lastError {
                            Text(err)
                                .font(.caption)
                                .foregroundStyle(.red)
                                .lineLimit(3)
                        }
                    }

                    TextField("Search models…", text: $searchText)
                        .textFieldStyle(.roundedBorder)
                }

                if store.isLoading && store.subscriptions.isEmpty {
                    HStack {
                        ProgressView()
                        Text("Loading models...")
                            .font(.footnote)
                            .foregroundStyle(.secondary)
                    }
                } else if store.subscriptions.isEmpty {
                    Text("No models loaded. Click Detect to scan local auth files.")
                        .font(.footnote)
                        .foregroundStyle(.tertiary)
                } else {
                    ForEach(filteredSubscriptions) { subscription in
                        SettingsSection(title: subscription.provider.capitalized) {
                            ForEach(subscription.models) { model in
                                modelRow(model)
                            }
                        }
                    }
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.vertical, 4)
        }
        .task {
            await store.fetch(baseURL: settings.baseURL, secret: settings.managementSecret)
        }
    }

    @ViewBuilder
    private func modelRow(_ model: SubscriptionModel) -> some View {
        HStack(spacing: 10) {
            Toggle("", isOn: Binding(
                get: { store.exposedModels.contains(model.id) },
                set: { _ in
                    Task {
                        await store.toggle(model.id, baseURL: settings.baseURL, secret: settings.managementSecret)
                    }
                }
            ))
            .toggleStyle(.checkbox)
            .labelsHidden()

            VStack(alignment: .leading, spacing: 2) {
                Text(model.displayName ?? model.id)
                    .font(.body)
                Text(model.id)
                    .font(.system(.caption, design: .monospaced))
                    .foregroundStyle(.tertiary)
            }

            Spacer()
        }
        .padding(.vertical, 2)
    }
}
