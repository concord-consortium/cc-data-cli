# Read-only. Finds portal classes that have RUN a CLUE assignment whose unit
# contains the Dataflow tile.
#
#   docker compose exec app bundle exec rails runner /path/to/find_dataflow_classes.rb
#
# Writes CSV to /tmp/dataflow_classes.csv and prints a summary.
#
# Unit codes come from clue-curriculum analysis; aliases from CLUE's
# curriculum-config.json unitCodeMap (a portal URL may use either spelling).

require 'csv'
require 'uri'

# code => [aliases that resolve to it]
TARGET_UNITS = {
  # students are instructed to add/create Dataflow tiles
  'brain'   => ['neural-engineering'],
  'vibe'    => [],
  'tinker'  => [],
  'seeit'   => [],
  # Dataflow tile embedded in curriculum, but NOT in the unit toolbar
  'clueful' => [],
  # demo/example units - included to see whether any real class ran them
  'dfe'     => ['dataflow-example'],
}.freeze

DATAFLOW_PROBLEMS = {
  'brain'   => %w[0.1 1.2 1.3 1.4 1.5 2.2 2.3 3.3 3.4],
  'vibe'    => %w[0.1 0.2 1.2 1.4 1.5 1.6 1.7 2.2 2.3 3.3 3.4],
  'tinker'  => %w[0.1],
  'seeit'   => %w[1.1 1.2],
  'clueful' => %w[1.3],
  'dfe'     => %w[1.1],
}.freeze

ALL_SPELLINGS = TARGET_UNITS.flat_map { |code, aliases| [code] + aliases }.freeze
CODE_FOR = TARGET_UNITS.each_with_object({}) { |(code, aliases), h|
  h[code] = code
  aliases.each { |a| h[a] = code }
}.freeze

def unit_and_problem(url)
  q = URI.parse(url.to_s).query
  return [nil, nil] unless q
  params = URI.decode_www_form(q).to_h
  [params['unit'], params['problem']]
rescue StandardError
  [nil, nil]
end

puts "== Step 1: CLUE ExternalActivities =="
clue = ExternalActivity.where("url LIKE ?", "%collaborative-learning%")
puts "  external activities with a collaborative-learning URL: #{clue.count}"

matched = []
clue.find_each do |ea|
  unit, problem = unit_and_problem(ea.url)
  next if unit.nil?
  code = CODE_FOR[unit]
  next if code.nil?
  matched << { ea: ea, unit_param: unit, code: code, problem: problem }
end

puts "  of those, using a Dataflow unit: #{matched.size}"
matched.group_by { |m| m[:code] }.sort.each do |code, ms|
  probs = ms.map { |m| m[:problem] }.tally.sort.map { |p, n| "#{p || 'none'}(#{n})" }
  puts "    #{code}: #{ms.size} activities  problems: #{probs.join(' ')}"
end

if matched.empty?
  puts "\nNo matching activities. Widen the Step 1 LIKE - CLUE may be assigned"
  puts "under a different host. Sample of CLUE-ish URLs:"
  ExternalActivity.where("url LIKE ?", "%clue%").limit(10).pluck(:id, :url).each { |i, u| puts "  #{i}: #{u}" }
  exit
end

puts "\n== Step 2: classes that RAN them =="
ids = matched.map { |m| m[:ea].id }
info_for_runnable = matched.to_h { |m|
  df = (DATAFLOW_PROBLEMS[m[:code]] || []).include?(m[:problem].to_s)
  [m[:ea].id, { code: m[:code], problem: m[:problem], has_dataflow: df }]
}

rl = Report::Learner.where(runnable_type: 'ExternalActivity', runnable_id: ids)
                  .where.not(last_run: nil)
puts "  report_learners with a last_run: #{rl.count}"

rows = []
rl.group_by { |r| [r.class_id, r.runnable_id] }.each do |(class_id, runnable_id), learners|
  s = learners.first
  info = info_for_runnable[runnable_id]
  rows << {
    unit:          info[:code],
    problem:       info[:problem],
    has_dataflow:  info[:has_dataflow],
    class_id:      class_id,
    class_name:    s.class_name,
    school:        s.school_name,
    teachers:      s.teachers_name,
    activity_id:   runnable_id,
    activity_name: s.runnable_name,
    students_run:  learners.map(&:student_id).uniq.size,
    first_run:     learners.map(&:last_run).compact.min,
    last_run:      learners.map(&:last_run).compact.max,
  }
end

rows.sort_by! { |r| [r[:unit].to_s, r[:problem].to_s, r[:last_run] ? -r[:last_run].to_i : 0] }

df_rows = rows.select { |r| r[:has_dataflow] }
puts "\n  -- restricted to problems that actually involve Dataflow --"
puts "  (class, activity) pairs: #{df_rows.size}"
puts "  distinct classes: #{df_rows.map { |r| r[:class_id] }.uniq.size}"
df_rows.group_by { |r| r[:unit] }.sort.each do |unit, rs|
  puts "    #{unit}: #{rs.map { |r| r[:class_id] }.uniq.size} classes, " \
       "#{rs.sum { |r| r[:students_run] }} student-runs, " \
       "problems #{rs.map { |r| r[:problem] }.uniq.sort.join(',')}"
end

puts "\n===CSV_BEGIN==="
if rows.any?
  puts CSV.generate_line(rows.first.keys)
  rows.each { |r| print CSV.generate_line(r.values) }
end
puts "===CSV_END==="

puts "  distinct (class, activity) pairs: #{rows.size}"
puts "  distinct classes: #{rows.map { |r| r[:class_id] }.uniq.size}"
puts "\n  by unit:"
rows.group_by { |r| r[:unit] }.sort.each do |unit, rs|
  puts "    #{unit}: #{rs.map { |r| r[:class_id] }.uniq.size} classes, " \
       "#{rs.sum { |r| r[:students_run] }} student-runs, " \
       "#{rs.map { |r| r[:last_run] }.compact.min&.to_date} .. " \
       "#{rs.map { |r| r[:last_run] }.compact.max&.to_date}"
end
